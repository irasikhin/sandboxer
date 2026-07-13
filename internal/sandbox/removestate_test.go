package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRemoveStateKeepsHome: RemoveState(keepHome=true) wipes the working state
// (sandbox dir, logs) but preserves the private agent home AND the registration
// (agents.list, active marker) — those belong to Remove.
func TestRemoveStateKeepsHome(t *testing.T) {
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(b.SandboxDir("s"), "f.txt"), "x")
	writeFile(t, filepath.Join(b.HomeDir("s"), "cred.json"), "tok")
	writeFile(t, b.LogPath("s", "json"), "{}")
	if err := b.AppendAgent("s"); err != nil {
		t.Fatal(err)
	}
	if err := b.SetCurrent("s"); err != nil {
		t.Fatal(err)
	}

	b.RemoveState("s", true)

	for _, p := range []string{b.SandboxDir("s"), b.LogPath("s", "json")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("path still present after RemoveState: %s", p)
		}
	}
	if _, err := os.Stat(filepath.Join(b.HomeDir("s"), "cred.json")); err != nil {
		t.Error("keepHome=true must preserve the agent home")
	}
	if len(b.Agents()) != 1 || b.Current() != "s" {
		t.Errorf("RemoveState must not touch registration: agents=%v current=%q", b.Agents(), b.Current())
	}

	b.RemoveState("s", false)
	if _, err := os.Stat(b.HomeDir("s")); !os.IsNotExist(err) {
		t.Error("keepHome=false must remove the agent home")
	}
}
