package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
)

// stateDir returns the runtime-state directory for a project root, as tests
// expect it after the config/data split (state lives under config.StateDir, not
// under the project's .sandboxer/, which now holds only the committed config).
func stateDir(project string, parts ...string) string {
	return filepath.Join(append([]string{config.StateDir(project)}, parts...)...)
}

// TestMain isolates runtime state into a throwaway directory so the cli suite
// never writes into the developer's real ~/.local/state. SANDBOXER_STATE wins
// over HOME/XDG_STATE_HOME, so tests that set their own HOME still get a state
// dir under this root; config.StateDir(project) yields the matching path, which
// tests use to locate the per-project state.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sandboxer-cli-state-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("SANDBOXER_STATE", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
