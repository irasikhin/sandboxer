package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCreateFromNamedProfile drives the headline flow: a named profile in the
// global store, used by name to create a sandbox whose slug comes from the
// profile.
func TestCreateFromNamedProfile(t *testing.T) {
	project := newProject(t) // also points SANDBOXER_PROFILES at an empty temp dir
	store := os.Getenv("SANDBOXER_PROFILES")
	if err := os.WriteFile(filepath.Join(store, "web.yaml"),
		[]byte("name: web\nbackend: docker\nagent: claude\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errs := run("create", "web", "--src", project)
	if code != 0 || !strings.Contains(out, "web") {
		t.Fatalf("create web = (%d, %q, %q)", code, out, errs)
	}
	if fi, err := os.Stat(filepath.Join(project, ".sandboxer", "web")); err != nil || !fi.IsDir() {
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
		[]byte("name: api\nbackend: docker\nagent: claude\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, errs := run("create", "api", "-f", envs, "--src", project); code != 0 || !strings.Contains(out, "api") {
		t.Fatalf("create api -f dir = (%d, %q, %q)", code, out, errs)
	}
	if _, err := os.Stat(filepath.Join(project, ".sandboxer", "api")); err != nil {
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
	dir := t.TempDir()
	t.Setenv("SANDBOXER_PROFILES", dir)
	if err := os.WriteFile(filepath.Join(dir, "web.yaml"),
		[]byte("name: web\nbackend: docker\nagent: claude\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Lists the global store.
	if code, out, _ := run("profiles"); code != 0 || !strings.Contains(out, "web") {
		t.Errorf("profiles = (%d, %q)", code, out)
	}
	// Empty store reports so cleanly.
	t.Setenv("SANDBOXER_PROFILES", t.TempDir())
	if code, out, _ := run("profiles"); code != 0 || !strings.Contains(out, "no profiles") {
		t.Errorf("profiles (empty) = (%d, %q)", code, out)
	}
	// A -f directory overrides the store.
	if code, out, _ := run("profiles", "-f", dir); code != 0 || !strings.Contains(out, "web") {
		t.Errorf("profiles -f dir = (%d, %q)", code, out)
	}
}
