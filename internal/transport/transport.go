// Package transport keeps the agent connected to the panel over WebSocket:
// it registers on first use, reports host info and metrics, and reconnects
// with exponential backoff.
//
// The agent always dials out and never listens. Panel messages are parsed
// against a whitelist of two types; anything else closes the connection.
package transport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/kergeio/kerge-agent/internal/config"
	"github.com/kergeio/kerge-protocol"
)

// Defaults for the timeouts and the reconnect backoff.
const (
	DefaultHandshakeTimeout = 10 * time.Second
	DefaultRegisterTimeout  = 10 * time.Second
	DefaultWriteTimeout     = 5 * time.Second
	DefaultMinBackoff       = time.Second
	DefaultMaxBackoff       = 60 * time.Second
)

// Options configure a Client.
type Options struct {
	// Server is the panel endpoint from agent.conf.
	Server string
	// Token is the one-time enrollment token, empty once used.
	Token string
	// CredentialPath is the file holding the long-lived credential.
	CredentialPath string
	// HTTPClient dials the handshake; zero means the default client.
	HTTPClient *http.Client
	// Logger receives connection events; zero means slog.Default().
	Logger *slog.Logger

	HandshakeTimeout time.Duration
	RegisterTimeout  time.Duration
	WriteTimeout     time.Duration
	MinBackoff       time.Duration
	MaxBackoff       time.Duration

	// randFloat returns a number in [0,1) for the backoff jitter; zero
	// means the default source.
	randFloat func() float64
}

// Client maintains the connection to the panel.
type Client struct {
	opts Options
	q    queue
	// enrollAgain records that the panel rejected the stored credential,
	// so the next attempt registers again.
	enrollAgain atomic.Bool
}

// New checks the options and returns a Client. It does not connect.
func New(opts Options) (*Client, error) {
	if err := config.ValidateServer(opts.Server); err != nil {
		return nil, err
	}
	if opts.CredentialPath == "" {
		return nil, errors.New("transport: no credential path")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	setDefault(&opts.HandshakeTimeout, DefaultHandshakeTimeout)
	setDefault(&opts.RegisterTimeout, DefaultRegisterTimeout)
	setDefault(&opts.WriteTimeout, DefaultWriteTimeout)
	setDefault(&opts.MinBackoff, DefaultMinBackoff)
	setDefault(&opts.MaxBackoff, DefaultMaxBackoff)
	if opts.randFloat == nil {
		opts.randFloat = rand.Float64
	}
	c := &Client{opts: opts}
	c.q.signal = make(chan struct{}, 1)
	return c, nil
}

func setDefault(d *time.Duration, v time.Duration) {
	if *d <= 0 {
		*d = v
	}
}

// Send queues a sample for the panel, replacing one that has not been sent
// yet. Nothing is buffered beyond that single sample, so a reconnecting
// agent never replays a backlog.
func (c *Client) Send(m *protocol.Metrics) { c.q.put(m) }

// Run connects and keeps the connection up until ctx is done. hostInfo is
// called once per connection, because the panel expects host info at the
// start of every connection.
func (c *Client) Run(ctx context.Context, hostInfo func(context.Context) *protocol.HostInfo) {
	attempt := 0
	for {
		established, err := c.session(ctx, hostInfo)
		if ctx.Err() != nil {
			return
		}
		if established {
			attempt = 0
		}
		attempt++
		if err != nil {
			c.opts.Logger.Warn("connection to the panel ended", "error", err)
		}
		wait := c.backoff(attempt)
		c.opts.Logger.Info("reconnecting to the panel", "wait", wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// session runs one connection. established reports whether host info was
// sent, which is what resets the backoff.
func (c *Client) session(ctx context.Context, hostInfo func(context.Context) *protocol.HostInfo) (established bool, err error) {
	enrolling, authorization, err := c.authorization()
	if err != nil {
		return false, err
	}

	dialCtx, cancelDial := context.WithTimeout(ctx, c.opts.HandshakeTimeout)
	conn, resp, err := websocket.Dial(dialCtx, c.opts.Server, &websocket.DialOptions{
		HTTPClient: c.opts.HTTPClient,
		HTTPHeader: http.Header{"Authorization": []string{authorization}},
		// The protocol version is agreed on in the handshake; a panel
		// that shares none of these refuses it.
		Subprotocols: protocol.Supported(),
		// Metrics are repetitive JSON, so compression cuts the daily
		// outbound traffic to about a quarter.
		CompressionMode: websocket.CompressionContextTakeover,
	})
	cancelDial()
	if err != nil {
		if !enrolling && resp != nil && resp.StatusCode == http.StatusUnauthorized {
			c.credentialRejected()
		}
		return false, err
	}
	defer conn.CloseNow()
	// The library accepts a handshake in which the panel echoed no
	// version at all, so the agreement is checked here.
	if v := conn.Subprotocol(); !protocol.Supports(v) {
		return false, fmt.Errorf("transport: the panel agreed to no protocol version this agent speaks: %q", v)
	}
	conn.SetReadLimit(protocol.MaxAgentRead)

	ctx, stop := context.WithCancel(ctx)
	defer stop()

	if enrolling {
		if err := c.register(ctx, conn); err != nil {
			return false, err
		}
	}
	if err := c.write(ctx, conn, hostInfo(ctx)); err != nil {
		return false, err
	}

	readErr := make(chan error, 1)
	go func() { readErr <- c.read(ctx, conn, enrolling) }()

	for {
		select {
		case <-ctx.Done():
			return true, nil
		case err := <-readErr:
			return true, err
		case <-c.q.signal:
			m := c.q.take()
			if m == nil {
				continue
			}
			if err := c.write(ctx, conn, m); err != nil {
				return true, err
			}
		}
	}
}

// authorization decides which credential to present.
func (c *Client) authorization() (enrolling bool, value string, err error) {
	credential, err := loadCredential(c.opts.CredentialPath)
	if err != nil {
		return false, "", err
	}
	switch {
	case c.opts.Token != "" && (credential == "" || c.enrollAgain.Load()):
		return true, protocol.EnrollAuthorization(c.opts.Token), nil
	case credential != "":
		return false, protocol.AgentAuthorization(credential), nil
	default:
		return false, "", errors.New("transport: no stored credential and no TOKEN in the configuration")
	}
}

// credentialRejected makes the next attempt register again, which is how an
// agent recovers after the panel reset its access.
func (c *Client) credentialRejected() {
	if c.opts.Token == "" {
		return
	}
	if c.enrollAgain.CompareAndSwap(false, true) {
		c.opts.Logger.Warn("the panel rejected the stored credential; registering again with the configured token")
	}
}

// register waits for the registration result and stores the credential.
func (c *Client) register(ctx context.Context, conn *websocket.Conn) error {
	readCtx, cancel := context.WithTimeout(ctx, c.opts.RegisterTimeout)
	defer cancel()
	msg, err := c.readOne(readCtx, conn)
	if err != nil {
		return fmt.Errorf("transport: waiting for the registration result: %w", err)
	}
	reg, ok := msg.(*protocol.Registered)
	if !ok {
		return panelMessageError(msg)
	}
	if err := saveCredential(c.opts.CredentialPath, reg.AgentID+"."+reg.Secret); err != nil {
		return fmt.Errorf("transport: storing the credential: %w", err)
	}
	c.enrollAgain.Store(false)
	c.opts.Logger.Info("registered with the panel", "agent_id", reg.AgentID)
	return nil
}

// read waits for a panel message. Reading is also what lets the library
// answer the panel's pings. During normal operation the panel sends
// nothing: registered belongs to enrollment and an error ends the
// connection, so any message at all terminates the session.
func (c *Client) read(ctx context.Context, conn *websocket.Conn, enrolled bool) error {
	msg, err := c.readOne(ctx, conn)
	if err != nil {
		return err
	}
	if e, ok := msg.(*protocol.ErrorMessage); ok && e.Code == protocol.CodeUnauthorized && !enrolled {
		c.credentialRejected()
	}
	return panelMessageError(msg)
}

// readOne reads and decodes one panel message. A message that is not text,
// is too large, or does not decode strictly ends the connection.
func (c *Client) readOne(ctx context.Context, conn *websocket.Conn) (protocol.PanelMessage, error) {
	typ, data, err := conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	if typ != websocket.MessageText {
		return nil, fmt.Errorf("transport: the panel sent a %v message", typ)
	}
	return protocol.DecodePanelMessage(data)
}

// panelMessageError turns a well-formed but unexpected message into the
// error that ends the connection.
func panelMessageError(msg protocol.PanelMessage) error {
	switch m := msg.(type) {
	case *protocol.ErrorMessage:
		return fmt.Errorf("transport: the panel reported %s: %s", m.Code, m.Message)
	case *protocol.Registered:
		return errors.New("transport: the panel sent an unexpected registration result")
	default:
		return fmt.Errorf("transport: unexpected %T from the panel", msg)
	}
}

// messageWriter is the writing half of a connection, so that tests can
// observe the deadline every write carries.
type messageWriter interface {
	Write(ctx context.Context, typ websocket.MessageType, p []byte) error
}

// write sends one message under the write timeout. A timeout ends the
// connection instead of stalling the agent.
func (c *Client) write(ctx context.Context, conn messageWriter, msg any) error {
	data, err := protocol.Marshal(msg)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, c.opts.WriteTimeout)
	defer cancel()
	return conn.Write(writeCtx, websocket.MessageText, data)
}

// backoff returns how long to wait before attempt n, using full jitter so
// that the agents of a restarted panel do not reconnect in lockstep.
func (c *Client) backoff(attempt int) time.Duration {
	d := c.opts.MinBackoff
	for range attempt - 1 {
		if d >= c.opts.MaxBackoff/2 {
			d = c.opts.MaxBackoff
			break
		}
		d *= 2
	}
	return time.Duration(c.opts.randFloat() * float64(d))
}

// queue holds at most one sample: a new one replaces the one waiting.
type queue struct {
	mu     sync.Mutex
	item   *protocol.Metrics
	signal chan struct{}
}

func (q *queue) put(m *protocol.Metrics) {
	q.mu.Lock()
	q.item = m
	q.mu.Unlock()
	select {
	case q.signal <- struct{}{}:
	default:
	}
}

func (q *queue) take() *protocol.Metrics {
	q.mu.Lock()
	defer q.mu.Unlock()
	m := q.item
	q.item = nil
	return m
}
