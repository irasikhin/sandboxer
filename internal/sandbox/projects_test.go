package sandbox

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
)

// TestProjects: the state root IS the host-wide index — every dir carrying a
// _meta/run.env is a project, its recorded SRC is the project root, and the
// project-independent records that live beside them (machines/, images/) are
// not projects. A recorded root that no longer exists is REPORTED (Gone), not
// dropped: its state dir is exactly the leftover a user needs to see.
func TestProjects(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SANDBOXER_STATE", root)

	live := t.TempDir()
	gone := filepath.Join(t.TempDir(), "deleted-project")
	if err := os.MkdirAll(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{live, gone} {
		if _, err := ResolveBase(src); err != nil {
			t.Fatalf("ResolveBase(%s): %v", src, err)
		}
	}
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	// Neither of these is a project: the microVM machine registry sits beside
	// the project dirs under the same root, and a stray file has no _meta at all.
	if err := os.MkdirAll(filepath.Join(root, "machines"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "stray"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// A project dir with no SRC recorded cannot be pointed at, so it is skipped.
	noSrc := filepath.Join(root, "nosrc-000000000000", "_meta")
	if err := os.MkdirAll(noSrc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(noSrc, "run.env"), []byte("DOMAINS=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := Projects()
	if len(got) != 2 {
		t.Fatalf("Projects() = %v, want exactly the 2 project dirs", got)
	}
	if got[0].Src > got[1].Src {
		t.Errorf("Projects() is not sorted by project root: %q then %q", got[0].Src, got[1].Src)
	}
	bySrc := map[string]Project{}
	for _, p := range got {
		bySrc[p.Src] = p
	}
	for src, wantGone := range map[string]bool{live: false, gone: true} {
		p, ok := bySrc[src]
		if !ok {
			t.Fatalf("Projects() has no entry for %s: %v", src, got)
		}
		if p.Gone != wantGone {
			t.Errorf("Projects()[%s].Gone = %v, want %v", src, p.Gone, wantGone)
		}
		if want := config.StateDir(src); p.Dir != want {
			t.Errorf("Projects()[%s].Dir = %q, want %q", src, p.Dir, want)
		}
		// The embedded Base is usable read-only, which is the whole point: a
		// host-wide listing reads each project's sandboxes through it.
		if agents := p.Agents(); len(agents) != 0 {
			t.Errorf("Projects()[%s].Agents() = %v, want none", src, agents)
		}
	}
}

// TestProjectsNoIndex: an absent or unusable state root yields nothing rather
// than an error — a listing must still print what it can find.
func TestProjectsNoIndex(t *testing.T) {
	t.Run("root does not exist", func(t *testing.T) {
		t.Setenv("SANDBOXER_STATE", filepath.Join(t.TempDir(), "never-created"))
		if got := Projects(); got != nil {
			t.Errorf("Projects() = %v, want nil", got)
		}
	})

	t.Run("no root resolvable", func(t *testing.T) {
		t.Setenv("SANDBOXER_STATE", "")
		t.Setenv("XDG_STATE_HOME", "")
		t.Setenv("HOME", "")
		if got := Projects(); got != nil {
			t.Errorf("Projects() = %v, want nil", got)
		}
	})
}
