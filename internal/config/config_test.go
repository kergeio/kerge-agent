package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kergeio/kerge-protocol/ifacefilter"
)

func parse(t *testing.T, body string) (*Config, error) {
	t.Helper()
	return Parse(strings.NewReader(body))
}

func TestParseFullFile(t *testing.T) {
	cfg, err := parse(t, `# Kerge agent configuration
SERVER=wss://panel.example.com/api/agent/ws
TOKEN=abc123
INTERVAL=10
IFACE_EXCLUDE=lo,docker*,veth*
`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server != "wss://panel.example.com/api/agent/ws" {
		t.Errorf("Server = %q", cfg.Server)
	}
	if cfg.Token != "abc123" {
		t.Errorf("Token = %q", cfg.Token)
	}
	if cfg.Interval != 10*time.Second {
		t.Errorf("Interval = %v", cfg.Interval)
	}
	if !slices.Equal(cfg.IfaceExclude, []string{"lo", "docker*", "veth*"}) {
		t.Errorf("IfaceExclude = %v", cfg.IfaceExclude)
	}
}

func TestParseDefaults(t *testing.T) {
	cfg, err := parse(t, "SERVER=wss://panel.example.com/api/agent/ws\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Interval != DefaultInterval {
		t.Errorf("Interval = %v, want %v", cfg.Interval, DefaultInterval)
	}
	if !slices.Equal(cfg.IfaceExclude, ifacefilter.DefaultExclude) {
		t.Errorf("IfaceExclude = %v", cfg.IfaceExclude)
	}
	if cfg.Token != "" {
		t.Errorf("Token = %q, want empty", cfg.Token)
	}
}

// An empty value excludes nothing, which is different from leaving the key
// out.
func TestEmptyIfaceExcludeExcludesNothing(t *testing.T) {
	cfg, err := parse(t, "SERVER=wss://p.example.com/ws\nIFACE_EXCLUDE=\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.IfaceExclude) != 0 {
		t.Errorf("IfaceExclude = %v, want none", cfg.IfaceExclude)
	}
}

func TestParseIgnoresCommentsAndBlankLines(t *testing.T) {
	cfg, err := parse(t, "\n# comment\n   \n\t# indented comment\r\nSERVER=wss://p.example.com/ws\r\n\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server != "wss://p.example.com/ws" {
		t.Errorf("Server = %q", cfg.Server)
	}
}

func TestParseTrimsWhitespaceAroundKeyAndValue(t *testing.T) {
	cfg, err := parse(t, "  SERVER = wss://p.example.com/ws  \nINTERVAL\t=\t7\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server != "wss://p.example.com/ws" || cfg.Interval != 7*time.Second {
		t.Errorf("Server = %q, Interval = %v", cfg.Server, cfg.Interval)
	}
}

// Values are literal: no quote handling and no variable expansion.
func TestParseDoesNotInterpretValues(t *testing.T) {
	cfg, err := parse(t, "SERVER=wss://p.example.com/ws\nTOKEN=\"$HOME\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != `"$HOME"` {
		t.Errorf("Token = %q", cfg.Token)
	}
}

func TestParseRejects(t *testing.T) {
	const server = "SERVER=wss://p.example.com/ws\n"
	cases := map[string]string{
		"unknown key":           server + "EXEC=/bin/sh\n",
		"lower case key":        server + "token=abc\n",
		"duplicate key":         server + "TOKEN=a\nTOKEN=b\n",
		"duplicate server":      server + server,
		"no equals sign":        server + "TOKEN\n",
		"empty key":             server + "=value\n",
		"missing server":        "TOKEN=abc\n",
		"empty server":          "SERVER=\n",
		"server without host":   "SERVER=wss://\n",
		"server scheme http":    "SERVER=https://p.example.com/ws\n",
		"server no scheme":      "SERVER=p.example.com/ws\n",
		"server credentials":    "SERVER=wss://user:pw@p.example.com/ws\n",
		"ws remote host":        "SERVER=ws://p.example.com/ws\n",
		"ws private ip":         "SERVER=ws://192.168.1.10:3000/ws\n",
		"interval not a number": server + "INTERVAL=fast\n",
		"interval fractional":   server + "INTERVAL=5.5\n",
		"interval too low":      server + "INTERVAL=2\n",
		"interval too high":     server + "INTERVAL=61\n",
		"interval empty":        server + "INTERVAL=\n",
		"empty pattern":         server + "IFACE_EXCLUDE=lo,,eth0\n",
		"trailing comma":        server + "IFACE_EXCLUDE=lo,\n",
		"pattern with space":    server + "IFACE_EXCLUDE=eth 0\n",
		"too many patterns":     server + "IFACE_EXCLUDE=" + strings.Repeat("lo,", ifacefilter.MaxPatterns) + "lo\n",
		"pattern too long":      server + "IFACE_EXCLUDE=" + strings.Repeat("e", ifacefilter.MaxPatternLen+1) + "\n",
		"token with space":      server + "TOKEN=a b\n",
		"token too long":        server + "TOKEN=" + strings.Repeat("a", maxTokenLen+1) + "\n",
		"line too long":         server + "TOKEN=" + strings.Repeat("a", maxLineLen) + "\n",
	}
	for name, in := range cases {
		if _, err := parse(t, in); err == nil {
			t.Errorf("%s: parsed without error", name)
		}
	}
}

func TestParseAcceptsLoopbackWithoutTLS(t *testing.T) {
	for _, server := range []string{
		"ws://localhost:3000/api/agent/ws",
		"ws://127.0.0.1:3000/api/agent/ws",
		"ws://[::1]:3000/api/agent/ws",
		"wss://panel.example.com/api/agent/ws",
	} {
		if _, err := parse(t, "SERVER="+server+"\n"); err != nil {
			t.Errorf("%s: %v", server, err)
		}
	}
}

// Errors must say which line is wrong, and Load must name the file.
func TestErrorsLocateTheProblem(t *testing.T) {
	_, err := parse(t, "SERVER=wss://p.example.com/ws\n\n# c\nEXEC=sh\n")
	if err == nil || !strings.Contains(err.Error(), "line 4") || !strings.Contains(err.Error(), `"EXEC"`) {
		t.Fatalf("err = %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "agent.conf")
	if err := os.WriteFile(path, []byte("EXEC=sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("Load err = %v", err)
	}

	if _, err := Load(filepath.Join(dir, "missing.conf")); err == nil {
		t.Error("Load of a missing file succeeded")
	}
}

func TestLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.conf")
	body := "SERVER=wss://p.example.com/api/agent/ws\nTOKEN=t\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "t" || cfg.Interval != DefaultInterval {
		t.Errorf("cfg = %+v", cfg)
	}
}
