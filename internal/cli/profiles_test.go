package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
)

// TestCreateFromNamedProfile drives the headline flow: a named profile in the
// global store, used by name to create a sandbox whose slug comes from the
// profile.
func TestCreateFromNamedProfile(t *testing.T) {
	project := newProject(t) // also points SANDBOXER_PROFILES at an empty temp dir
	store := os.Getenv("SANDBOXER_PROFILES")
	if err := os.WriteFile(filepath.Join(store, "web.yaml"),
		[]byte("name: web\nbackend: docker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errs := run("create", "web", "--src", project)
	if code != 0 || !strings.Contains(out, "web") {
		t.Fatalf("create web = (%d, %q, %q)", code, out, errs)
	}
	if fi, err := os.Stat(stateDir(project, "web")); err != nil || !fi.IsDir() {
		t.Errorf("sandbox dir for named profile not created: %v", err)
	}
	// The resolved profile is stored, so `show` reports it (not "no profile").
	if code, out, _ := run("show", "web", "--src", project); code != 0 || strings.Contains(out, "no profile") {
		t.Errorf("show web = (%d, %q)", code, out)
	}
}

// TestCreateFromProfileDir picks a profile out of a -f directory by name.
func TestCreateFromProfileDir(t *testing.T) {
	project := newProject(t)
	envs := t.TempDir()
	if err := os.WriteFile(filepath.Join(envs, "api.yaml"),
		[]byte("name: api\nbackend: docker\nsrcs: [{src: .}]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, errs := run("create", "api", "-f", envs, "--src", project); code != 0 || !strings.Contains(out, "api") {
		t.Fatalf("create api -f dir = (%d, %q, %q)", code, out, errs)
	}
	if _, err := os.Stat(stateDir(project, "api")); err != nil {
		t.Errorf("sandbox dir for dir-selected profile not created: %v", err)
	}
	// An unknown name in the directory fails with the available listing.
	if code, _, errs := run("create", "ghost", "-f", envs, "--src", project); code != 1 || !strings.Contains(errs, "no profile") {
		t.Errorf("create unknown -f dir = (%d, %q)", code, errs)
	}
}

func TestSelectFromDir(t *testing.T) {
	// An empty directory has nothing to select.
	if _, err := selectFromDir(t.TempDir(), ""); err == nil {
		t.Error("empty dir should error")
	}
	dir := t.TempDir()
	write := func(n, body string) {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A single profile with no name given is used implicitly.
	write("one.yaml", "name: one\n")
	if got, err := selectFromDir(dir, ""); err != nil || got != filepath.Join(dir, "one.yaml") {
		t.Errorf("single profile = (%q,%v)", got, err)
	}
	// With more than one, an empty name is ambiguous.
	write("two.yaml", "name: two\n")
	if _, err := selectFromDir(dir, ""); err == nil {
		t.Error("multiple profiles with no name should error")
	}
}

func TestProfilesCommand(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	// Isolate the project config (read relative to the cwd) so the listing
	// reflects only the fixtures, never the host's.
	t.Chdir(t.TempDir())

	store := t.TempDir()
	t.Setenv("SANDBOXER_PROFILES", store)
	if err := os.WriteFile(filepath.Join(store, "web.yaml"),
		[]byte("name: web\nbackend: docker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The store profile is listed and tagged as its source.
	if code, out, _ := run("profile", "list"); code != 0 || !strings.Contains(out, "web") || !strings.Contains(out, "store") {
		t.Errorf("profile list (store) = (%d, %q)", code, out)
	}

	// A project sandboxer.yaml is listed too, tagged project — the gap
	// this command had before (it only ever read the store).
	if err := os.WriteFile(config.ConfigPath(),
		[]byte("name: feat\nbackend: docker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := run("profile", "list"); code != 0 || !strings.Contains(out, "feat") || !strings.Contains(out, "project") {
		t.Errorf("profile list (project) = (%d, %q)", code, out)
	}

	// The default: profile is marked with the word "(default)" — not the `*`
	// glyph, which `list` already uses for the active sandbox — and the shadow
	// legend only prints when something is actually shadowed.
	if err := os.WriteFile(config.ConfigPath(),
		[]byte("profiles:\n  feat:\n    backend: docker\n  api:\n    backend: docker\ndefault: feat\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := run("profile", "list"); code != 0 || !strings.Contains(out, "feat (default)") || strings.Contains(out, "shadowed") {
		t.Errorf("profile list default marker = (%d, %q)", code, out)
	}

	// A -f directory overrides the sources and lists just that dir (no project).
	if code, out, _ := run("profile", "list", "-f", store); code != 0 || !strings.Contains(out, "web") || strings.Contains(out, "feat") {
		t.Errorf("profile list -f dir = (%d, %q)", code, out)
	}

	// Nothing in any source reports the actionable hint.
	t.Chdir(t.TempDir())
	t.Setenv("SANDBOXER_PROFILES", t.TempDir())
	if code, out, _ := run("profile", "list"); code != 0 || !strings.Contains(out, "no profiles found") {
		t.Errorf("profile list (empty) = (%d, %q)", code, out)
	}
}
