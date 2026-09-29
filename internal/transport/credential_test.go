package transport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCredentialRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	if got, err := loadCredential(path); err != nil || got != "" {
		t.Fatalf("loadCredential before registering = %q, %v", got, err)
	}
	const value = "host1.s3cret"
	if err := saveCredential(path, value); err != nil {
		t.Fatal(err)
	}
	got, err := loadCredential(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != value {
		t.Errorf("loadCredential = %q, want %q", got, value)
	}
	// Writing again replaces the file and leaves no temporary behind.
	if err := saveCredential(path, "host1.newer"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "credential" {
		t.Errorf("directory holds %v, want only the credential", entries)
	}
}

// A truncated or edited file fails at startup instead of turning into a
// puzzling authentication failure.
func TestLoadCredentialRejectsBadContent(t *testing.T) {
	cases := map[string]string{
		"empty":         "",
		"no separator":  "host1",
		"empty id":      ".s3cret",
		"empty secret":  "host1.",
		"two dots":      "host1.s3.cret",
		"with a space":  "host 1.s3cret",
		"control chars": "host1.s3\x00cret",
	}
	for name, content := range cases {
		path := filepath.Join(t.TempDir(), "credential")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadCredential(path); err == nil {
			t.Errorf("%s: loadCredential succeeded", name)
		} else if !strings.Contains(err.Error(), path) {
			t.Errorf("%s: error does not name the file: %v", name, err)
		}
		if err := saveCredential(path, content); err == nil {
			t.Errorf("%s: saveCredential succeeded", name)
		}
	}
}

// Trailing whitespace from a hand-edited file is tolerated.
func TestLoadCredentialTrimsWhitespace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(path, []byte("  host1.s3cret\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadCredential(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != "host1.s3cret" {
		t.Errorf("loadCredential = %q", got)
	}
}
