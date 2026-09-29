// Package config parses the agent's KEY=value configuration file. It
// deliberately has no dependencies: an unknown key, a duplicate key or a
// malformed line is a startup failure with a clear error.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kergeio/kerge-protocol/ifacefilter"
)

// DefaultPath is where install-agent.sh writes the configuration.
const DefaultPath = "/etc/kerge-agent/agent.conf"

// Interval bounds and default, in seconds.
const (
	MinInterval     = 3 * time.Second
	MaxInterval     = 60 * time.Second
	DefaultInterval = 5 * time.Second
)

const (
	maxLineLen  = 4096
	maxTokenLen = 256
)

// Config is the parsed configuration.
type Config struct {
	// Server is the panel WebSocket endpoint.
	Server string
	// Token is the one-time enrollment token. It is empty once the agent
	// has a long-lived credential, and the file may omit it.
	Token string
	// Interval is the reporting interval.
	Interval time.Duration
	// IfaceExclude holds the wildcard patterns of interfaces not to report.
	// It is empty when the file excludes nothing.
	IfaceExclude []string
}

// Load reads and parses the file at path.
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cfg, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Parse reads the configuration from r. Values are taken literally: there is
// no quote handling and no variable expansion. Whitespace around the key and
// around the value is ignored; keys are case-sensitive.
func Parse(r io.Reader) (*Config, error) {
	cfg := &Config{Interval: DefaultInterval}
	seen := map[string]bool{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1024), maxLineLen)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		key, value, ok := strings.Cut(text, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: expected KEY=value", line)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if seen[key] {
			return nil, fmt.Errorf("line %d: duplicate key %q", line, key)
		}
		seen[key] = true
		if err := cfg.set(key, value); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, fmt.Errorf("line %d: line longer than %d bytes", line+1, maxLineLen)
		}
		return nil, err
	}
	if !seen["SERVER"] {
		return nil, errors.New("missing required key SERVER")
	}
	if !seen["IFACE_EXCLUDE"] {
		cfg.IfaceExclude = ifacefilter.DefaultExclude
	}
	return cfg, nil
}

func (c *Config) set(key, value string) error {
	switch key {
	case "SERVER":
		if err := ValidateServer(value); err != nil {
			return err
		}
		c.Server = value
	case "TOKEN":
		if len(value) > maxTokenLen {
			return fmt.Errorf("TOKEN longer than %d characters", maxTokenLen)
		}
		if strings.ContainsFunc(value, func(r rune) bool { return r <= ' ' || r > '~' }) {
			return errors.New("TOKEN must be printable ASCII without spaces")
		}
		c.Token = value
	case "INTERVAL":
		secs, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("INTERVAL must be a whole number of seconds, got %q", value)
		}
		d := time.Duration(secs) * time.Second
		if d < MinInterval || d > MaxInterval {
			return fmt.Errorf("INTERVAL must be between %d and %d seconds, got %d",
				int(MinInterval.Seconds()), int(MaxInterval.Seconds()), secs)
		}
		c.Interval = d
	case "IFACE_EXCLUDE":
		patterns, err := ifacefilter.ValidatePatterns(value)
		if err != nil {
			return fmt.Errorf("IFACE_EXCLUDE: %w", err)
		}
		c.IfaceExclude = patterns
	default:
		return fmt.Errorf("unknown key %q", key)
	}
	return nil
}

// ValidateServer accepts wss:// anywhere, and ws:// only for a loopback host.
func ValidateServer(value string) error {
	if value == "" {
		return errors.New("SERVER must not be empty")
	}
	u, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("SERVER is not a valid URL: %w", err)
	}
	if u.Host == "" {
		return errors.New("SERVER must include a host")
	}
	if u.User != nil {
		return errors.New("SERVER must not contain credentials")
	}
	switch u.Scheme {
	case "wss":
	case "ws":
		if !isLoopback(u.Hostname()) {
			return errors.New("SERVER may use ws:// only with a loopback host; use wss://")
		}
	default:
		return fmt.Errorf("SERVER must use ws:// or wss://, got %q", u.Scheme)
	}
	return nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
