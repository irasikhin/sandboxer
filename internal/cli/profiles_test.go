package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
)

// TestCreateFromProfileFile picks a profile from an explicit -f file; a -f
// directory is rejected (profiles live in one config file).
func TestCreateFromProfileFile(t *testing.T) {
	project := newProject(t)
	env := filepath.Join(t.TempDir(), "api.nix")
	if err := os.WriteFile(env,
		[]byte("{ name = \"api\"; backend = \"docker\"; srcs = [ { src = \".\"; } ]; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, errs := run("create", "-f", env, "--src", project); code != 0 || !strings.Contains(out, "api") {
		t.Fatalf("create -f file = (%d, %q, %q)", code, out, errs)
	}
	if _, err := os.Stat(stateDir(project, "api")); err != nil {
		t.Errorf("sandbox dir for -f profile not created: %v", err)
	}
	// A directory is refused with the one-config guidance.
	if code, _, errs := run("create", "x", "-f", t.TempDir(), "--src", project); code != 1 || !strings.Contains(errs, "one config file") {
		t.Errorf("create -f dir = (%d, %q), want the one-config-file refusal", code, errs)
	}
}

func TestProfilesCommand(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	// Isolate the project config (read relative to the cwd) so the listing
	// reflects only the fixtures, never the host's.
	t.Chdir(t.TempDir())

	// No config at all reports the actionable hint.
	if code, out, _ := run("profile", "list"); code != 0 || !strings.Contains(out, "no profiles in") {
		t.Errorf("profile list (empty) = (%d, %q)", code, out)
	}

	// A flat project sandboxer.nix lists its single profile.
	if err := os.WriteFile(config.ConfigPath(),
		[]byte("{ name = \"feat\"; backend = \"docker\"; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := run("profile", "list"); code != 0 || !strings.Contains(out, "feat") {
		t.Errorf("profile list (flat) = (%d, %q)", code, out)
	}

	// The default: profile is marked with the word "(default)" — not the `*`
	// glyph, which `list` already uses for the active sandbox.
	if err := os.WriteFile(config.ConfigPath(),
		[]byte("{ profiles = { feat.backend = \"docker\"; api.backend = \"docker\"; }; default = \"feat\"; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := run("profile", "list"); code != 0 || !strings.Contains(out, "feat (default)") || !strings.Contains(out, "api") {
		t.Errorf("profile list default marker = (%d, %q)", code, out)
	}

	// -f lists another file's sections instead of the project config.
	other := filepath.Join(t.TempDir(), "other.nix")
	if err := os.WriteFile(other, []byte("{ name = \"web\"; backend = \"podman\"; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := run("profile", "list", "-f", other); code != 0 || !strings.Contains(out, "web") || strings.Contains(out, "feat") {
		t.Errorf("profile list -f file = (%d, %q)", code, out)
	}
}
