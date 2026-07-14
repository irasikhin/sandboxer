package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
)

// fakeEditor writes an executable script that appends a marker to the file it is
// given, and points $EDITOR at it — so `image edit` / `profile edit` exercise
// openInEditor end to end (the editor runs and touches the file).
func fakeEditor(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "ed.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'EDITED\\n' >> \"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", script)
}

// TestProfileEditScaffoldsThenEdits: with no config, `profile edit` scaffolds
// the annotated starter, then runs $EDITOR on it.
func TestProfileEditScaffoldsThenEdits(t *testing.T) {
	t.Chdir(t.TempDir())
	fakeEditor(t)
	if code, _, errs := run("profile", "edit"); code != 0 {
		t.Fatalf("profile edit = %d, %s", code, errs)
	}
	body, err := os.ReadFile(config.ConfigPath())
	if err != nil {
		t.Fatalf("config not scaffolded: %v", err)
	}
	if !strings.Contains(string(body), "EDITED") {
		t.Errorf("editor did not run on the config:\n%s", body)
	}
	if !strings.Contains(string(body), "name:") {
		t.Errorf("scaffold missing expected content:\n%s", body)
	}
}

// TestImageEditScaffoldsThenEdits: `image edit` scaffolds image.nix when absent,
// then runs $EDITOR on it.
func TestImageEditScaffoldsThenEdits(t *testing.T) {
	t.Chdir(t.TempDir())
	fakeEditor(t)
	if code, _, errs := run("image", "edit"); code != 0 {
		t.Fatalf("image edit = %d, %s", code, errs)
	}
	body, err := os.ReadFile(imageNixPath())
	if err != nil {
		t.Fatalf("image.nix not scaffolded: %v", err)
	}
	if !strings.Contains(string(body), "EDITED") || !strings.Contains(string(body), "{ pkgs }") {
		t.Errorf("image edit did not scaffold+edit:\n%s", body)
	}
}

// TestEditorFailureSurfaces: a non-zero editor exit is reported as an error.
func TestEditorFailureSurfaces(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "false") // exits 1
	if code, _, errs := run("profile", "edit"); code != 1 || !strings.Contains(errs, "editor") {
		t.Errorf("profile edit with failing editor = (%d, %q), want exit 1 with an editor error", code, errs)
	}
}

// TestProfileValidate: a clean config validates; an unknown field is rejected;
// a missing file errors with a scaffold hint.
func TestProfileValidate(t *testing.T) {
	t.Chdir(t.TempDir())

	// Missing file.
	if code, _, errs := run("profile", "validate"); code != 1 || !strings.Contains(errs, "no config") {
		t.Errorf("validate missing = (%d, %q), want a no-config error", code, errs)
	}

	if err := os.MkdirAll(config.StateDirName, 0o755); err != nil {
		t.Fatal(err)
	}
	// Valid config.
	if err := os.WriteFile(config.ConfigPath(), []byte("name: ok\nbackend: docker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, errs := run("profile", "validate"); code != 0 || !strings.Contains(out, "ok") {
		t.Errorf("validate good = (%d, %q, %q)", code, out, errs)
	}
	// Unknown field is rejected (strict decode).
	if err := os.WriteFile(config.ConfigPath(), []byte("name: bad\nbogusField: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("profile", "validate"); code != 1 || errs == "" {
		t.Errorf("validate unknown-field = (%d, %q), want exit 1", code, errs)
	}
}

// TestImageRm: `image rm` resolves the engine + image and calls the removal
// seam; idempotent, prints what it removed.
func TestImageRm(t *testing.T) {
	fakePodman(t)
	t.Setenv("SANDBOXER_ENGINE", "podman")
	var got struct{ engine, image string }
	old := backendRemoveImage
	t.Cleanup(func() { backendRemoveImage = old })
	backendRemoveImage = func(engine, image string) error {
		got.engine, got.image = engine, image
		return nil
	}
	code, out, errs := run("image", "rm")
	if code != 0 {
		t.Fatalf("image rm = %d, %s", code, errs)
	}
	if got.engine != "podman" || got.image != config.DefaultImage {
		t.Errorf("image rm removed (%q, %q), want (podman, %q)", got.engine, got.image, config.DefaultImage)
	}
	if !strings.Contains(out, config.DefaultImage) {
		t.Errorf("image rm output = %q, want the removed image", out)
	}
}

// TestProfileUseAlias: `profile use` is the same selector as the top-level use.
func TestProfileUseAlias(t *testing.T) {
	project := newProject(t)
	if code, _, errs := run("profile", "use", "feat", "--src", project); code != 0 {
		t.Fatalf("profile use set = %d, %s", code, errs)
	}
	if code, out, _ := run("profile", "use", "--src", project); code != 0 || !strings.Contains(out, "feat") {
		t.Errorf("profile use get = (%d, %q), want feat", code, out)
	}
}

// TestHelpGroups: --help renders the activity groups.
func TestHelpGroups(t *testing.T) {
	code, out, _ := run("--help")
	if code != 0 {
		t.Fatalf("--help = %d", code)
	}
	for _, want := range []string{"Image & config:", "Sandbox (enter & work):", "Data (clean / show):", "image", "profile", "clean"} {
		if !strings.Contains(out, want) {
			t.Errorf("--help missing %q:\n%s", want, out)
		}
	}
}
