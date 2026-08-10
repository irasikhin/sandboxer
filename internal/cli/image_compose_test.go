package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/toolbox"
)

// stubVMBuild replaces the microVM image-build seam, capturing what the
// command hands it.
func stubVMBuild(t *testing.T) *struct {
	Engine, Image string
	Spec          toolbox.Spec
	Calls         int
} {
	t.Helper()
	captured := &struct {
		Engine, Image string
		Spec          toolbox.Spec
		Calls         int
	}{}
	old := backendBuildVMImage
	t.Cleanup(func() { backendBuildVMImage = old })
	backendBuildVMImage = func(engine, image string, spec toolbox.Spec, _ io.Writer) error {
		captured.Engine, captured.Image, captured.Spec = engine, image, spec
		captured.Calls++
		return nil
	}
	return captured
}

// TestBuildImageNoRunner: with neither runner installed, `image build` fails
// at engine resolution with the runner's install hint — never a silent no-op.
func TestBuildImageNoRunner(t *testing.T) {
	newProject(t)
	t.Setenv("SANDBOXER_MSB", "/nonexistent/msb-not-here")
	t.Setenv("SANDBOXER_BACKEND", "")
	if code, _, errs := run("image", "build"); code != 1 || !strings.Contains(errs, "msb") {
		t.Errorf("build-image no-runner = (%d, %q), want the msb install hint", code, errs)
	}
}

// TestBuildImageCommand drives the microVM-only build path end to end through
// the build seam: the stock default, a profile's variant, a multi-profile
// section, and the fail-fast error paths.
func TestBuildImageCommand(t *testing.T) {
	requireExec(t, "sh")
	newProject(t)
	fakeMsb(t)
	rev := strings.Repeat("f", 40)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	fakeGitRevs(t, rev, rev)
	captured := stubVMBuild(t)

	// The default build re-resolves the input revs (auto-update) and hands the
	// stock image to the VM build on the resolved default backend.
	if code, _, errs := run("image", "build"); code != 0 {
		t.Fatalf("build-image = %d %s", code, errs)
	}
	if captured.Engine != "microsandbox" {
		t.Errorf("build engine = %q, want microsandbox", captured.Engine)
	}
	if captured.Image != config.LoadDefaults().Image {
		t.Errorf("build image = %q, want the stock default", captured.Image)
	}

	// With a profile (-f): the profile's content-addressed variant tag is
	// built instead of the stock default (the progress banner names it).
	cfg := filepath.Join(t.TempDir(), "img.nix")
	if err := os.WriteFile(cfg, []byte("{ name = \"feat\"; srcs = [ { src = \".\"; branch = \"feat/x\"; } ]; image.packages = [ \"ripgrep\" ]; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errs := run("image", "build", "-f", cfg)
	if code != 0 {
		t.Fatalf("build-image -f = %d %s", code, errs)
	}
	if !strings.Contains(errs, "sandboxer-toolbox:var-") || !strings.HasPrefix(captured.Image, "sandboxer-toolbox:var-") {
		t.Errorf("expected a var- variant build, got image %q banner %s", captured.Image, errs)
	}

	// A multi-profile file: the positional names the section to build.
	multi := filepath.Join(t.TempDir(), "multi.nix")
	if err := os.WriteFile(multi,
		[]byte("{ profiles = { web.tools = [ \"node\" ]; plain = { }; }; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errs = run("image", "build", "web", "-f", multi)
	if code != 0 || !strings.Contains(errs, "sandboxer-toolbox:var-") {
		t.Errorf("build-image multi-profile = (%d, %q), want a var- build", code, errs)
	}

	// A positional that names no profile fails before any build work.
	if code, _, errs := run("image", "build", "no-such-profile"); code != 1 ||
		!strings.Contains(errs, "no profile") {
		t.Errorf("build-image bogus profile = (%d, %q)", code, errs)
	}

	// A profile whose image.overlay file is missing fails spec resolution fast.
	noNix := filepath.Join(t.TempDir(), "no-nix.nix")
	if err := os.WriteFile(noNix, []byte("{ name = \"feat\"; image.overlay = \"missing.nix\"; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("image", "build", "-f", noNix); code != 1 ||
		!strings.Contains(errs, "image.overlay") {
		t.Errorf("build-image missing image.overlay = (%d, %q)", code, errs)
	}

	// A malformed profile file fails the document load.
	broken := filepath.Join(t.TempDir(), "broken.nix")
	if err := os.WriteFile(broken, []byte("{ image = [ \"not-a-map\" ]; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("image", "build", "-f", broken); code != 1 || errs == "" {
		t.Errorf("build-image malformed profile = (%d, %q)", code, errs)
	}
}

// TestBuildImageRevFlags covers the rev plumbing end to end through the build
// seam: validation fails fast (before any runner), the default build
// re-resolves the tracking revs and stamps the pins cache while keeping the
// STOCK tag, --no-refresh builds from the warm stamp without a resolver, the
// default refresh fails loudly when the resolver is dead, and a concrete flag
// rev builds a variant.
func TestBuildImageRevFlags(t *testing.T) {
	requireExec(t, "sh")
	newProject(t)
	fakeMsb(t)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	// A malformed rev is rejected by the ValidateImageSpec rules before
	// profile or runner work.
	if code, _, errs := run("image", "build", "--llm-agents-rev", "ZZZ"); code != 1 ||
		!strings.Contains(errs, "image.llmAgentsRev") {
		t.Errorf("bad --llm-agents-rev = (%d, %q)", code, errs)
	}
	if code, _, errs := run("image", "build", "--nixpkgs-rev", "also bad"); code != 1 ||
		!strings.Contains(errs, "image.nixpkgsRev") {
		t.Errorf("bad --nixpkgs-rev = (%d, %q)", code, errs)
	}

	captured := stubVMBuild(t)

	rev := strings.Repeat("d", 40)
	fakeGitRevs(t, rev, rev)
	code, _, errs := run("image", "build")
	if code != 0 {
		t.Fatalf("build-image = %d %s", code, errs)
	}
	// The default build IS the auto-update: revs resolved and handed to the
	// build, under the stock tag ("latest" is the default, not a variant), and
	// no variant note.
	if strings.Contains(errs, "note: built variant") {
		t.Errorf("the default build must not print the variant note: %s", errs)
	}
	if captured.Spec.NixpkgsRev != rev || captured.Spec.LLMAgentsRev != rev {
		t.Errorf("seam spec revs = %q/%q, want the resolved %s", captured.Spec.NixpkgsRev, captured.Spec.LLMAgentsRev, rev)
	}
	if captured.Image != config.LoadDefaults().Image {
		t.Errorf("seam image = %q, want the stock default", captured.Image)
	}
	pins, err := toolbox.LoadPins()
	if err != nil || pins["nixpkgs"].Rev != rev {
		t.Errorf("stamped pins = %+v, %v; want nixpkgs %s", pins, err, rev)
	}
	// An explicit "latest" flag is the same default, spelled out — still the
	// stock tag, still no note.
	if code, _, errs := run("image", "build", "--nixpkgs-rev", "latest"); code != 0 ||
		strings.Contains(errs, "note: built variant") {
		t.Errorf("explicit latest = (%d, %q), want a plain stock build", code, errs)
	}
	if captured.Image != config.LoadDefaults().Image {
		t.Errorf("explicit-latest image = %q, want the stock default", captured.Image)
	}

	// --no-refresh: a dead git resolver does not matter — the warm stamp is
	// used, never re-resolved.
	failingGit(t)
	if code, _, errs := run("image", "build", "--no-refresh"); code != 0 {
		t.Errorf("no-refresh build-image = %d %s", code, errs)
	}
	if captured.Spec.NixpkgsRev != rev {
		t.Errorf("no-refresh spec rev = %q, want the stamped %s", captured.Spec.NixpkgsRev, rev)
	}

	// The default refresh re-resolves; a failing git run makes the resolve
	// fail loudly instead of silently reusing the stamp.
	if code, _, errs := run("image", "build"); code != 1 ||
		!strings.Contains(errs, "resolve latest") {
		t.Errorf("default refresh with a failing git = (%d, %q), want a resolve failure", code, errs)
	}

	// A concrete bare-flag rev is a pin → a variant nothing selects by
	// default; the command says so instead of implying the stock image moved.
	override := strings.Repeat("e", 40)
	if code, _, errs := run("image", "build", "--no-refresh",
		"--nixpkgs-rev", override, "--llm-agents-rev", override); code != 0 ||
		!strings.Contains(errs, "note: built variant") {
		t.Fatalf("concrete bare-flag build = (%d, %q), want the variant note", code, errs)
	}
	if captured.Spec.NixpkgsRev != override {
		t.Errorf("override spec rev = %q, want %s", captured.Spec.NixpkgsRev, override)
	}
	if captured.Image != captured.Spec.Tag() || !strings.HasPrefix(captured.Image, "sandboxer-toolbox:var-") {
		t.Errorf("seam image = %q, want the pinned spec's var- tag", captured.Image)
	}

	// A concrete flag rev is a one-shot override of the profile's tracking
	// value; the other input still resolves from the warm stamp.
	cfg := filepath.Join(t.TempDir(), "p.nix")
	if err := os.WriteFile(cfg, []byte("{ name = \"feat\"; image.nixpkgsRev = \"latest\"; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("image", "build", "--no-refresh", "-f", cfg, "--nixpkgs-rev", override); code != 0 {
		t.Fatalf("build-image concrete override = %d %s", code, errs)
	}
	if captured.Spec.NixpkgsRev != override || captured.Spec.LLMAgentsRev != rev {
		t.Errorf("override spec revs = %q/%q, want %s/%s", captured.Spec.NixpkgsRev, captured.Spec.LLMAgentsRev, override, rev)
	}
}
