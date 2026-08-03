package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
)

// fakeEditor writes an executable script that appends a marker to the file it
// is given, and points $EDITOR at it — so `config edit` exercises openInEditor
// end to end (the editor runs and touches the file).
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

// TestConfigEditScaffoldsThenEdits: with no config, `config edit` scaffolds
// the annotated starter (ONE file — the image hook lives inline in it), then
// runs $EDITOR on it.
func TestConfigEditScaffoldsThenEdits(t *testing.T) {
	t.Chdir(t.TempDir())
	fakeEditor(t)
	if code, _, errs := run("config", "edit"); code != 0 {
		t.Fatalf("config edit = %d, %s", code, errs)
	}
	body, err := os.ReadFile(config.ConfigPath())
	if err != nil {
		t.Fatalf("config not scaffolded: %v", err)
	}
	if !strings.Contains(string(body), "EDITED") {
		t.Errorf("editor did not run on the config:\n%s", body)
	}
	if !strings.Contains(string(body), "name =") {
		t.Errorf("scaffold missing expected content:\n%s", body)
	}
}

// TestEditorFailureSurfaces: a non-zero editor exit is reported as an error.
func TestEditorFailureSurfaces(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "false") // exits 1
	if code, _, errs := run("config", "edit"); code != 1 || !strings.Contains(errs, "editor") {
		t.Errorf("config edit with failing editor = (%d, %q), want exit 1 with an editor error", code, errs)
	}
}

// TestConfigValidate: a clean config validates; an unknown field is rejected;
// a missing file errors with a scaffold hint.
func TestConfigValidate(t *testing.T) {
	t.Chdir(t.TempDir())

	// Missing file.
	if code, _, errs := run("config", "validate"); code != 1 || !strings.Contains(errs, "no config") {
		t.Errorf("validate missing = (%d, %q), want a no-config error", code, errs)
	}

	// Valid config.
	if err := os.WriteFile(config.ConfigPath(), []byte("{ name = \"ok\"; backend = \"docker\"; srcs = [ { src = \".\"; branch = \"feat/x\"; } ]; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, errs := run("config", "validate"); code != 0 || !strings.Contains(out, "ok") {
		t.Errorf("validate good = (%d, %q, %q)", code, out, errs)
	}
	// Unknown attr is rejected (strict decode).
	if err := os.WriteFile(config.ConfigPath(), []byte("{ name = \"bad\"; bogusField = 1; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("config", "validate"); code != 1 || errs == "" {
		t.Errorf("validate unknown-field = (%d, %q), want exit 1", code, errs)
	}
}

// TestConfigValidateSemantic: validate runs the static semantic checks too —
// README promises a bad include/domain/backend fails HERE, not first at
// create/enter. Each case is a config that decodes fine but cannot run.
func TestConfigValidateSemantic(t *testing.T) {
	t.Chdir(t.TempDir())
	cases := []struct {
		desc, cfg, want string
	}{
		{"unknown backend",
			`{ name = "x"; backend = "dokcer"; srcs = [ { src = "."; branch = "b/x"; } ]; }`,
			"unknown backend"},
		{"domain missing dot",
			`{ name = "x"; srcs = [ { src = "."; branch = "b/x"; } ]; egress.allowedDomains = [ "githubcom" ]; }`,
			"missing dot"},
		{"negated include",
			`{ name = "x"; srcs = [ { src = "."; branch = "b/x"; include = [ "!/vendor/" ]; } ]; }`,
			"negation is not supported"},
		{"missing branch",
			`{ name = "x"; srcs = [ { src = "."; } ]; }`,
			"branch is required"},
		{"empty srcs",
			`{ name = "x"; srcs = [ ]; }`,
			"srcs is empty"},
		{"bad session",
			`{ name = "x"; session = "sticky"; srcs = [ { src = "."; branch = "b/x"; } ]; }`,
			"unknown session mode"},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			if err := os.WriteFile(config.ConfigPath(), []byte(c.cfg+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			code, _, errs := run("config", "validate")
			if code != 1 || !strings.Contains(errs, c.want) {
				t.Errorf("validate = (%d, %q), want exit 1 with %q", code, errs, c.want)
			}
		})
	}

	// A multi-profile file labels the failing section.
	multi := `{ profiles = { good = { srcs = [ { src = "."; branch = "b/x"; } ]; }; bad = { backend = "nope"; srcs = [ { src = "."; branch = "b/x"; } ]; }; }; default = "good"; }`
	if err := os.WriteFile(config.ConfigPath(), []byte(multi+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errs := run("config", "validate")
	if code != 1 || !strings.Contains(errs, "profiles.bad") {
		t.Errorf("multi validate = (%d, %q), want the failing section named", code, errs)
	}
}

// TestProfileDeprecatedAliases: the old `profile init|edit|validate`
// spellings still run (with a deprecation notice) but are hidden from help.
func TestProfileDeprecatedAliases(t *testing.T) {
	t.Chdir(t.TempDir())
	if code, out, errs := run("profile", "init"); code != 0 {
		t.Fatalf("profile init alias = %d, %s", code, errs)
	} else if !strings.Contains(out, "deprecated") {
		t.Errorf("profile init should print a deprecation notice, got %q", out)
	}
	if code, out, _ := run("profile", "validate"); code != 0 || !strings.Contains(out, "deprecated") {
		t.Errorf("profile validate alias = (%d, %q)", code, out)
	}
	code, out, _ := run("profile", "--help")
	if code != 0 {
		t.Fatalf("profile --help = %d", code)
	}
	for _, hidden := range []string{"init", "validate"} {
		if strings.Contains(out, "  "+hidden) {
			t.Errorf("deprecated alias %q should be hidden from profile --help:\n%s", hidden, out)
		}
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

// TestImageRmVariant: with a customized profile, rm resolves the variant tag
// via the warm pins stamp (never a resolver container); a cold stamp is a
// fail-closed error — nothing was ever built to remove.
func TestImageRmVariant(t *testing.T) {
	fakePodman(t)
	t.Setenv("SANDBOXER_ENGINE", "podman")
	cfg := filepath.Join(t.TempDir(), "img.nix")
	if err := os.WriteFile(cfg, []byte("{ name = \"feat\"; image.packages = [ \"ripgrep\" ]; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if code, _, errs := run("image", "rm", "-f", cfg); code != 1 || !strings.Contains(errs, "image build") {
		t.Errorf("variant rm on a cold pins cache = (%d, %q), want fail-closed guidance", code, errs)
	}

	warmPins(t, strings.Repeat("a", 40))
	var gotImage string
	old := backendRemoveImage
	t.Cleanup(func() { backendRemoveImage = old })
	backendRemoveImage = func(_, image string) error {
		gotImage = image
		return nil
	}
	if code, _, errs := run("image", "rm", "-f", cfg); code != 0 {
		t.Fatalf("variant rm = %d, %s", code, errs)
	}
	if !strings.HasPrefix(gotImage, "sandboxer-toolbox:var-") {
		t.Errorf("variant rm removed %q, want a var- tag", gotImage)
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
	for _, want := range []string{"Image & config:", "Sandbox (enter & work):", "Data (clean / show):", "image", "config", "profile", "clean"} {
		if !strings.Contains(out, want) {
			t.Errorf("--help missing %q:\n%s", want, out)
		}
	}
}
