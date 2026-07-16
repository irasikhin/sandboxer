package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/sandbox"
)

// stateDir returns the runtime-state directory for a project root, as tests
// expect it after the config/data split (state lives under config.StateDir, not
// at the project root, where the committed config lives).
func stateDir(project string, parts ...string) string {
	return filepath.Join(append([]string{config.StateDir(project)}, parts...)...)
}

// sandboxDir returns the sandbox worktree directory for a project root — the
// worktrees live BESIDE the project (<project>-sandboxes/), not in the state
// dir.
func sandboxDir(project string, parts ...string) string {
	return filepath.Join(append([]string{sandbox.SandboxesRoot(project)}, parts...)...)
}

// TestMain isolates runtime state into a throwaway directory so the cli suite
// never writes into the developer's real ~/.local/state. SANDBOXER_STATE wins
// over HOME/XDG_STATE_HOME, so tests that set their own HOME still get a state
// dir under this root; config.StateDir(project) yields the matching path, which
// tests use to locate the per-project state. HOME is isolated too: the
// scaffolded profile enables hostConfigs, and seeding must read a hermetic
// home — never the developer's real ~/.claude — unless a test plants one.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sandboxer-cli-state-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("SANDBOXER_STATE", dir)
	_ = os.Setenv("HOME", filepath.Join(dir, "home"))
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
