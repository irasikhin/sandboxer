package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCreateValidatesBeforeState pins create's fail-clean ordering: an invalid
// profile (image.ref plus customization here, but any ResolveRuntime rejection)
// must fail while create has made NOTHING — no worktrees, no profile snapshot,
// no sandbox visible to list. It used to write both first and diagnose over
// the half-created sandbox's corpse.
func TestCreateValidatesBeforeState(t *testing.T) {
	requireExec(t, "nix-instantiate")
	project := newProject(t)
	cfg := `{ srcs = [ { src = "."; branch = "t/x"; } ];
  image = { ref = "ghcr.io/x:1"; packages = [ "ripgrep" ]; }; }
`
	if err := os.WriteFile(filepath.Join(project, "sandboxer.nix"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, errs := run("create", "--src", project)
	if code == 0 || !strings.Contains(errs, "mutually exclusive") {
		t.Fatalf("create with an invalid profile = (%d, %q), want a validation refusal", code, errs)
	}
	if _, err := os.Stat(filepath.Join(project, "sandboxes")); !os.IsNotExist(err) {
		t.Error("create left a sandboxes root behind despite failing validation")
	}
	if code, out, _ := run("list", "--src", project); code != 0 || strings.Contains(out, "sandboxer") &&
		!strings.Contains(out, "no sandboxes") {
		// The slug for a flat config derives from the file name ("sandboxer").
		if strings.Contains(out, "ghcr.io/x:1") {
			t.Errorf("a half-created sandbox is visible in list:\n%s", out)
		}
	}
}
