package main

import "testing"

func TestStateDirectory(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	cases := []struct {
		name, flag string
		env        map[string]string
		want       string
	}{
		{"default", "", nil, defaultStateDir},
		{"from systemd", "", map[string]string{"STATE_DIRECTORY": "/var/lib/kerge-agent"}, "/var/lib/kerge-agent"},
		// systemd separates several StateDirectory= entries with colons.
		{"first of several", "", map[string]string{"STATE_DIRECTORY": "/var/lib/a:/var/lib/b"}, "/var/lib/a"},
		{"flag wins", "/tmp/state", map[string]string{"STATE_DIRECTORY": "/var/lib/a"}, "/tmp/state"},
	}
	for _, c := range cases {
		if got := stateDirectory(c.flag, env(c.env)); got != c.want {
			t.Errorf("%s: stateDirectory = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestVersionFlag(t *testing.T) {
	if err := run([]string{"--version"}, func(string) string { return "" }); err != nil {
		t.Errorf("--version: %v", err)
	}
	if err := run([]string{"--config", "/nonexistent/agent.conf"}, func(string) string { return "" }); err == nil {
		t.Error("a missing configuration file did not fail")
	}
}
