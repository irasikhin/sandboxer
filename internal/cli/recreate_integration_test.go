//go:build integration

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/itest"
)

// TestRecreate_Container_KeepsHome_RealEngine drives `recreate` against a real
// engine and proves its home semantics end to end: a marker written into the
// sandbox-private $HOME survives a plain recreate (the agent's login/state is
// preserved when the sandbox is rebuilt) but is wiped by `recreate --full`
// (equivalent to rm + create). Egress off, so it needs only the toolbox image.
func TestRecreate_Container_KeepsHome_RealEngine(t *testing.T) {
	engine := itest.Engine(t)
	image := itest.EnsureToolboxImage(t, engine)
	t.Setenv("SANDBOXER_ENGINE", engine)
	t.Setenv("SANDBOXER_IMAGE", image)
	t.Setenv("SANDBOXER_NO_EGRESS", "1")
	t.Setenv("SANDBOXER_NO_AUTOBUILD", "1")

	project := newProject(t)
	t.Setenv("HOME", t.TempDir())
	cfg := filepath.Join(t.TempDir(), "sbx.nix")
	if err := os.WriteFile(cfg, []byte("{ name = \"feat\"; backend = \""+engine+"\"; srcs = [ { src = \".\"; branch = \"feat/x\"; } ]; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if code, out, errs := run("create", "--src", project, "--config", cfg); code != 0 {
		t.Fatalf("create = %d\nout: %s\nerr: %s", code, out, errs)
	}
	// Marker in the private home (bound rw at $HOME inside the container).
	if code, _, errs := run("exec", "feat", "--src", project, "--config", cfg,
		"--ephemeral", "--", "bash", "-c", "echo kept > $HOME/marker"); code != 0 {
		t.Fatalf("write marker = %d\n%s", code, errs)
	}

	// Plain recreate: the sandbox is rebuilt but the private home is preserved.
	if code, _, errs := run("recreate", "feat", "--src", project, "--config", cfg); code != 0 {
		t.Fatalf("recreate = %d\n%s", code, errs)
	}
	if code, out, errs := run("exec", "feat", "--src", project, "--config", cfg,
		"--ephemeral", "--", "bash", "-c", "cat $HOME/marker 2>/dev/null || echo GONE"); code != 0 || !strings.Contains(out, "kept") {
		t.Fatalf("marker after recreate = (%d, %q) — a plain recreate must keep the home\n%s", code, out, errs)
	}

	// recreate --full wipes the private home (rm + create).
	if code, _, errs := run("recreate", "feat", "--src", project, "--config", cfg, "--full"); code != 0 {
		t.Fatalf("recreate --full = %d\n%s", code, errs)
	}
	if code, out, errs := run("exec", "feat", "--src", project, "--config", cfg,
		"--ephemeral", "--", "bash", "-c", "cat $HOME/marker 2>/dev/null || echo GONE"); code != 0 || !strings.Contains(out, "GONE") {
		t.Errorf("marker after recreate --full = (%d, %q) — --full must wipe the home\n%s", code, out, errs)
	}
}
