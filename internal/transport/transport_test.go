package transport

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/kergeio/kerge-protocol"
)

const (
	testToken  = "enrollment-token"
	testAgent  = "host1"
	testSecret = "s3cret"
)

// panel is a stand-in for the panel's agent endpoint. Each accepted
// connection is handed to the test, which drives it directly.
type panel struct {
	t      *testing.T
	server *httptest.Server
	conns  chan *panelConn
	status atomic.Int32
	// silentVersion makes the panel upgrade the connection without
	// echoing a protocol version, which no agreement means.
	silentVersion atomic.Bool
	// offered records the versions of the last handshake.
	offered atomic.Pointer[[]string]
}

type panelConn struct {
	auth string
	conn *websocket.Conn
	// silent records that this connection was upgraded without a version.
	silent  bool
	release chan struct{}
}

func newPanel(t *testing.T) *panel {
	t.Helper()
	p := &panel{t: t, conns: make(chan *panelConn, 16)}
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s := p.status.Load(); s != 0 {
			http.Error(w, "rejected", int(s))
			return
		}
		offered := r.Header.Values(protocol.SubprotocolHeader)
		p.offered.Store(&offered)
		version, ok := protocol.SelectVersion(offered)
		if !ok {
			http.Error(w, "unsupported protocol version", http.StatusBadRequest)
			return
		}
		silent := p.silentVersion.Load()
		var negotiated []string
		if !silent {
			negotiated = []string{version}
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols:    negotiated,
			CompressionMode: websocket.CompressionContextTakeover,
		})
		if err != nil {
			return
		}
		pc := &panelConn{auth: r.Header.Get("Authorization"), conn: conn, silent: silent, release: make(chan struct{})}
		select {
		case p.conns <- pc:
		default:
			conn.CloseNow()
			return
		}
		<-pc.release
		conn.CloseNow()
	}))
	t.Cleanup(p.server.Close)
	return p
}

// url returns the loopback ws:// endpoint, which is allowed without TLS.
func (p *panel) url() string {
	return "ws" + strings.TrimPrefix(p.server.URL, "http") + "/api/agent/ws"
}

// accept waits for the next connection.
func (p *panel) accept() *panelConn {
	p.t.Helper()
	select {
	case c := <-p.conns:
		p.t.Cleanup(func() { c.close() })
		return c
	case <-time.After(5 * time.Second):
		p.t.Fatal("timed out waiting for the agent to connect")
		return nil
	}
}

func (c *panelConn) close() {
	select {
	case <-c.release:
	default:
		close(c.release)
	}
}

// recv reads one agent message.
func (c *panelConn) recv(t *testing.T) protocol.AgentMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	typ, data, err := c.conn.Read(ctx)
	if err != nil {
		t.Fatalf("reading from the agent: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("the agent sent a %v message", typ)
	}
	msg, err := protocol.DecodeAgentMessage(data)
	if err != nil {
		t.Fatalf("decoding the agent message: %v", err)
	}
	return msg
}

func (c *panelConn) send(t *testing.T, msg any) {
	t.Helper()
	data, err := protocol.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	c.sendRaw(t, data)
}

func (c *panelConn) sendRaw(t *testing.T, data []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := c.conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("writing to the agent: %v", err)
	}
}

func testClient(t *testing.T, p *panel, token, credential string) (*Client, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credential")
	if credential != "" {
		if err := saveCredential(path, credential); err != nil {
			t.Fatal(err)
		}
	}
	c, err := New(Options{
		Server:         p.url(),
		Token:          token,
		CredentialPath: path,
		Logger:         slog.New(slog.DiscardHandler),
		MinBackoff:     time.Millisecond,
		MaxBackoff:     5 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c, path
}

func hostInfo(context.Context) *protocol.HostInfo {
	return &protocol.HostInfo{Hostname: "web-1", OS: "linux", CPUCores: 2, IntervalMS: 5000}
}

// start runs the client until the test ends.
func start(t *testing.T, c *Client) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx, hostInfo)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return after the context was cancelled")
		}
	})
}

func sample(ts int64) *protocol.Metrics {
	return &protocol.Metrics{TS: ts, MonoMS: ts * 1000}
}

// A fresh agent registers with the configured token, stores the credential
// and goes straight on to reporting on the same connection.
func TestEnrollment(t *testing.T) {
	p := newPanel(t)
	c, path := testClient(t, p, testToken, "")
	start(t, c)

	conn := p.accept()
	if got, want := conn.auth, "Bearer enroll:"+testToken; got != want {
		t.Errorf("Authorization = %q, want %q", got, want)
	}
	conn.send(t, &protocol.Registered{AgentID: testAgent, Secret: testSecret})

	if _, ok := conn.recv(t).(*protocol.HostInfo); !ok {
		t.Fatal("the agent did not send host_info after registering")
	}
	c.Send(sample(7))
	m, ok := conn.recv(t).(*protocol.Metrics)
	if !ok || m.TS != 7 {
		t.Fatalf("got %+v, want the queued sample", m)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(data)), testAgent+"."+testSecret; got != want {
		t.Errorf("credential file = %q, want %q", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != credentialMode {
		t.Errorf("credential mode = %o, want %o", got, credentialMode)
	}
}

// Once registered, the agent presents the long-lived credential and does
// not wait for a registration result.
func TestReconnectUsesStoredCredential(t *testing.T) {
	p := newPanel(t)
	c, _ := testClient(t, p, testToken, testAgent+"."+testSecret)
	start(t, c)

	conn := p.accept()
	if got, want := conn.auth, "Bearer agent:"+testAgent+"."+testSecret; got != want {
		t.Errorf("Authorization = %q, want %q", got, want)
	}
	if _, ok := conn.recv(t).(*protocol.HostInfo); !ok {
		t.Fatal("the agent did not send host_info")
	}
}

// Host info is sent again on every connection, because the panel keeps it
// per connection.
func TestReconnectAfterTheConnectionDrops(t *testing.T) {
	p := newPanel(t)
	c, _ := testClient(t, p, "", testAgent+"."+testSecret)
	start(t, c)

	first := p.accept()
	if _, ok := first.recv(t).(*protocol.HostInfo); !ok {
		t.Fatal("the agent did not send host_info")
	}
	first.close()

	second := p.accept()
	if _, ok := second.recv(t).(*protocol.HostInfo); !ok {
		t.Fatal("the agent did not send host_info after reconnecting")
	}
	c.Send(sample(9))
	if m, ok := second.recv(t).(*protocol.Metrics); !ok || m.TS != 9 {
		t.Fatalf("got %+v, want the sample after reconnecting", m)
	}
}

// Anything the agent does not recognise closes the connection, and the
// agent reconnects rather than crashing.
func TestBadPanelMessagesCloseTheConnection(t *testing.T) {
	cases := map[string][]byte{
		"unknown type":           []byte(`{"type":"exec","cmd":"sh"}`),
		"unknown field":          []byte(`{"type":"error","code":"x","message":"y","run":"sh"}`),
		"malformed json":         []byte(`{"type":"error",`),
		"too large":              []byte(`{"type":"error","code":"x","message":"` + strings.Repeat("a", protocol.MaxAgentRead) + `"}`),
		"registered out of turn": []byte(`{"type":"registered","agent_id":"a","secret":"b"}`),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			p := newPanel(t)
			c, _ := testClient(t, p, "", testAgent+"."+testSecret)
			start(t, c)

			first := p.accept()
			first.recv(t)
			first.sendRaw(t, payload)

			second := p.accept()
			if _, ok := second.recv(t).(*protocol.HostInfo); !ok {
				t.Fatal("the agent did not reconnect")
			}
		})
	}
}

// A message past the agent's read limit is cut off at the WebSocket layer,
// so the agent never buffers it, and it closes with 1009.
func TestOversizedPanelMessageIsCutOff(t *testing.T) {
	p := newPanel(t)
	c, _ := testClient(t, p, "", testAgent+"."+testSecret)
	start(t, c)

	first := p.accept()
	first.recv(t)
	first.sendRaw(t, []byte(`{"type":"error","code":"x","message":"`+strings.Repeat("a", 1<<20)+`"}`))

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, _, err := first.conn.Read(ctx)
	if got := websocket.CloseStatus(err); got != websocket.StatusMessageTooBig {
		t.Errorf("close status = %v, want %v (error %v)", got, websocket.StatusMessageTooBig, err)
	}

	second := p.accept()
	if _, ok := second.recv(t).(*protocol.HostInfo); !ok {
		t.Fatal("the agent did not reconnect")
	}
}

// Every write carries the write timeout, so a stalled link ends the
// connection instead of the agent.
func TestWriteAppliesTheTimeout(t *testing.T) {
	c, err := New(Options{
		Server:         "wss://panel.example.com/ws",
		CredentialPath: "/tmp/credential",
		WriteTimeout:   3 * time.Second,
		Logger:         slog.New(slog.DiscardHandler),
	})
	if err != nil {
		t.Fatal(err)
	}
	var w recordingWriter
	before := time.Now()
	if err := c.write(t.Context(), &w, sample(1)); err != nil {
		t.Fatal(err)
	}
	if !w.hasDeadline {
		t.Fatal("the write ran without a deadline")
	}
	if got := w.deadline.Sub(before); got > 4*time.Second || got < 2*time.Second {
		t.Errorf("deadline in %v, want about %v", got, c.opts.WriteTimeout)
	}
	if string(w.payload[:len(`{"type":"metrics"`)]) != `{"type":"metrics"` {
		t.Errorf("payload = %s", w.payload)
	}
}

type recordingWriter struct {
	deadline    time.Time
	hasDeadline bool
	payload     []byte
}

func (w *recordingWriter) Write(ctx context.Context, _ websocket.MessageType, p []byte) error {
	w.deadline, w.hasDeadline = ctx.Deadline()
	w.payload = p
	return nil
}

// When the panel resets a host's access, the stored credential stops
// working. The agent falls back to the token from agent.conf and
// re-registers.
func TestCredentialRejectedFallsBackToTheToken(t *testing.T) {
	t.Run("error message", func(t *testing.T) {
		p := newPanel(t)
		c, path := testClient(t, p, testToken, testAgent+".stale")
		start(t, c)

		first := p.accept()
		if !strings.HasPrefix(first.auth, "Bearer agent:") {
			t.Fatalf("Authorization = %q", first.auth)
		}
		first.recv(t)
		first.send(t, &protocol.ErrorMessage{Code: protocol.CodeUnauthorized, Message: "revoked"})

		second := p.accept()
		if got, want := second.auth, "Bearer enroll:"+testToken; got != want {
			t.Fatalf("Authorization = %q, want %q", got, want)
		}
		second.send(t, &protocol.Registered{AgentID: testAgent, Secret: "fresh"})
		if _, ok := second.recv(t).(*protocol.HostInfo); !ok {
			t.Fatal("the agent did not send host_info after re-registering")
		}
		data, _ := os.ReadFile(path)
		if got, want := strings.TrimSpace(string(data)), testAgent+".fresh"; got != want {
			t.Errorf("credential file = %q, want %q", got, want)
		}
	})

	t.Run("handshake 401", func(t *testing.T) {
		p := newPanel(t)
		p.status.Store(http.StatusUnauthorized)
		c, _ := testClient(t, p, testToken, testAgent+".stale")
		start(t, c)

		// Let a few rejected handshakes go by, then accept again.
		time.Sleep(50 * time.Millisecond)
		p.status.Store(0)
		conn := p.accept()
		if got, want := conn.auth, "Bearer enroll:"+testToken; got != want {
			t.Fatalf("Authorization = %q, want %q", got, want)
		}
	})
}

// Without a token there is nothing to fall back to, so the agent keeps
// trying the credential it has.
func TestCredentialRejectedWithoutAToken(t *testing.T) {
	p := newPanel(t)
	c, _ := testClient(t, p, "", testAgent+".stale")
	start(t, c)

	first := p.accept()
	first.recv(t)
	first.send(t, &protocol.ErrorMessage{Code: protocol.CodeUnauthorized, Message: "revoked"})

	second := p.accept()
	if !strings.HasPrefix(second.auth, "Bearer agent:") {
		t.Errorf("Authorization = %q, want the stored credential", second.auth)
	}
}

// The send queue holds one sample: a new one replaces the one waiting, so a
// slow or broken link never builds a backlog.
func TestQueueKeepsOnlyTheNewestSample(t *testing.T) {
	var q queue
	q.signal = make(chan struct{}, 1)
	for i := range int64(10) {
		q.put(sample(i))
	}
	m := q.take()
	if m == nil || m.TS != 9 {
		t.Fatalf("take = %+v, want the newest sample", m)
	}
	if got := q.take(); got != nil {
		t.Errorf("take = %+v, want nothing left", got)
	}
	if len(q.signal) != 1 {
		t.Errorf("signal has %d entries, want 1", len(q.signal))
	}
}

// Samples produced while the agent is disconnected are not replayed: the
// panel only ever sees the newest one.
func TestDisconnectedSamplesAreNotReplayed(t *testing.T) {
	p := newPanel(t)
	c, _ := testClient(t, p, "", testAgent+"."+testSecret)
	start(t, c)

	first := p.accept()
	first.recv(t)
	first.close()

	for i := range int64(20) {
		c.Send(sample(i))
	}
	second := p.accept()
	if _, ok := second.recv(t).(*protocol.HostInfo); !ok {
		t.Fatal("the agent did not reconnect")
	}
	c.Send(sample(100))
	m, ok := second.recv(t).(*protocol.Metrics)
	if !ok {
		t.Fatalf("got %+v, want a sample", m)
	}
	if m.TS < 19 {
		t.Errorf("first sample after reconnecting has ts %d, want no backlog", m.TS)
	}
}

func TestNewRejectsBadOptions(t *testing.T) {
	cases := map[string]Options{
		"no server":       {CredentialPath: "/tmp/c"},
		"remote ws":       {Server: "ws://panel.example.com/ws", CredentialPath: "/tmp/c"},
		"http scheme":     {Server: "https://panel.example.com/ws", CredentialPath: "/tmp/c"},
		"no credentials":  {Server: "wss://panel.example.com/ws"},
		"url credentials": {Server: "wss://u:p@panel.example.com/ws", CredentialPath: "/tmp/c"},
	}
	for name, opts := range cases {
		if _, err := New(opts); err == nil {
			t.Errorf("%s: New succeeded", name)
		}
	}
	if _, err := New(Options{Server: "wss://panel.example.com/ws", CredentialPath: "/tmp/c"}); err != nil {
		t.Errorf("valid options rejected: %v", err)
	}
}

// Backoff doubles up to the cap and is fully jittered, so the agents of a
// restarted panel do not reconnect in lockstep.
func TestBackoff(t *testing.T) {
	c, err := New(Options{
		Server:         "wss://panel.example.com/ws",
		CredentialPath: "/tmp/c",
		MinBackoff:     time.Second,
		MaxBackoff:     60 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ceilings := []time.Duration{1, 2, 4, 8, 16, 32, 60, 60, 60}
	for i, want := range ceilings {
		attempt := i + 1
		c.opts.randFloat = func() float64 { return 0.9999 }
		if got := c.backoff(attempt); got > want*time.Second || got < want*time.Second*9/10 {
			t.Errorf("attempt %d: backoff = %v, want just under %v", attempt, got, want*time.Second)
		}
		c.opts.randFloat = func() float64 { return 0 }
		if got := c.backoff(attempt); got != 0 {
			t.Errorf("attempt %d: backoff = %v with no jitter, want 0", attempt, got)
		}
	}
}

// The agent names the protocol version it speaks in the handshake, so that
// a panel which speaks none of them can refuse before the upgrade.
func TestHandshakeOffersTheProtocolVersion(t *testing.T) {
	p := newPanel(t)
	c, _ := testClient(t, p, testToken, testAgent+"."+testSecret)
	start(t, c)

	conn := p.accept()
	if _, ok := conn.recv(t).(*protocol.HostInfo); !ok {
		t.Fatal("the agent did not send host_info")
	}
	offered := p.offered.Load()
	if offered == nil || len(*offered) != 1 || (*offered)[0] != protocol.Version {
		t.Errorf("offered versions = %v, want [%q]", offered, protocol.Version)
	}
	if got := conn.conn.Subprotocol(); got != protocol.Version {
		t.Errorf("negotiated subprotocol = %q, want %q", got, protocol.Version)
	}
}

// A panel that upgrades the connection but echoes no version has agreed to
// nothing, so the agent drops the connection and retries instead of
// reporting into it.
func TestPanelThatAgreesToNoVersion(t *testing.T) {
	p := newPanel(t)
	p.silentVersion.Store(true)
	// A stored credential means the agent would report immediately, so a
	// connection that stays quiet is one the agent has abandoned.
	c, _ := testClient(t, p, testToken, testAgent+"."+testSecret)
	start(t, c)

	conn := p.accept()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	began := time.Now()
	if _, _, err := conn.conn.Read(ctx); err == nil {
		t.Fatal("the agent reported on a connection with no agreed version")
	}
	if waited := time.Since(began); waited > 2*time.Second {
		t.Fatalf("the agent neither reported nor closed the connection within %v", waited)
	}

	// Once the panel agrees on a version the agent reports as usual.
	p.silentVersion.Store(false)
	for {
		next := p.accept()
		if next.silent {
			next.close()
			continue
		}
		if _, ok := next.recv(t).(*protocol.HostInfo); !ok {
			t.Fatal("the agent did not report after the version was agreed on")
		}
		return
	}
}
