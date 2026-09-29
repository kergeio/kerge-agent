package transport

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kergeio/kerge-protocol"
)

// credentialMode is the permission of the credential file.
const credentialMode = 0o600

// loadCredential reads the stored "<agent_id>.<secret>" value. It returns an
// empty string when the agent has not registered yet.
func loadCredential(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(data))
	if err := checkCredential(value); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return value, nil
}

// checkCredential applies the same rules the panel used when it issued the
// credential, so a truncated or edited file fails at startup rather than as
// a puzzling authentication failure.
func checkCredential(value string) error {
	agentID, secret, ok := strings.Cut(value, ".")
	if !ok {
		return errors.New("credential must be <agent_id>.<secret>")
	}
	reg := protocol.Registered{AgentID: agentID, Secret: secret}
	if err := reg.Validate(); err != nil {
		return err
	}
	return nil
}

// saveCredential writes the credential so that a crash cannot leave a
// half-written file behind.
func saveCredential(path, value string) error {
	if err := checkCredential(value); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".credential-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(credentialMode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(value + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	// Fsync the directory so that the rename survives a power loss.
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
