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

// fakeMsbOnPath points SANDBOXER_MSB at a dummy executable so
// ResolveEngine("microsandbox") resolves without a real msb.
func fakeMsbOnPath(t *testing.T) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "msb")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SANDBOXER_MSB", bin)
}

// TestBuildImageMsbRoutesToVMBuild: `image build` builds with host nix into
// the microVM store, with pins resolved on the HOST via git (no engine
// anywhere), and the resolved revs reach the VM build's spec.
func TestBuildImageMsbRoutesToVMBuild(t *testing.T) {
	requireExec(t, "sh")
	newProject(t)
	fakeMsbOnPath(t)
	rev := strings.Repeat("b", 40)
	fakeGitRevs(t, rev)

	var vmImage string
	var vmSpec toolbox.Spec
	oldVM := backendBuildVMImage
	defer func() { backendBuildVMImage = oldVM }()
	backendBuildVMImage = func(_, image string, spec toolbox.Spec, _ io.Writer) error {
		vmImage = image
		vmSpec = spec
		return nil
	}

	if code, _, errs := run("image", "build", "--backend", "microsandbox"); code != 0 {
		t.Fatalf("build --backend microsandbox = %d %s", code, errs)
	}
	if vmImage != config.DefaultImage {
		t.Errorf("vm build image = %q, want %q", vmImage, config.DefaultImage)
	}
	if vmSpec.NixpkgsRev != rev {
		t.Errorf("vm build spec rev = %q, want the resolved %s", vmSpec.NixpkgsRev, rev)
	}
}

// TestBuildImageMsbNoEngine: with no container engine at all and a COLD pins
// cache, `image build` still works: the revs resolve via host git and the
// build is handed to the VM backend.
func TestBuildImageMsbNoEngine(t *testing.T) {
	newProject(t)
	fakeMsbOnPath(t)

	oldVM := backendBuildVMImage
	defer func() { backendBuildVMImage = oldVM }()
	backendBuildVMImage = func(_, _ string, _ toolbox.Spec, _ io.Writer) error { return nil }

	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	fakeGitRevs(t, strings.Repeat("a", 40))
	if code, _, errs := run("image", "build"); code != 0 {
		t.Errorf("cold cache, no container engine = (%d, %q), want a successful build", code, errs)
	}
}

// TestBuildImageMsbVariant: a profile with image customization builds its
// content-addressed variant tag.
func TestBuildImageMsbVariant(t *testing.T) {
	requireExec(t, "sh")
	newProject(t)
	fakeMsbOnPath(t)
	fakeGitRevs(t, strings.Repeat("b", 40))

	var vmImage string
	oldVM := backendBuildVMImage
	defer func() { backendBuildVMImage = oldVM }()
	backendBuildVMImage = func(_, image string, _ toolbox.Spec, _ io.Writer) error {
		vmImage = image
		return nil
	}

	cfg := filepath.Join(t.TempDir(), "img.nix")
	if err := os.WriteFile(cfg, []byte("{ name = \"feat\"; backend = \"microsandbox\"; image.packages = [ \"ripgrep\" ]; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// No --backend flag: the backend comes from the profile.
	if code, _, errs := run("image", "build", "-f", cfg); code != 0 {
		t.Fatalf("build -f (microsandbox profile) = %d %s", code, errs)
	}
	if !strings.Contains(vmImage, "sandboxer-toolbox:var-") {
		t.Errorf("vm build image = %q, want a var- variant", vmImage)
	}
}

// TestBuildImageNoMsb: a missing msb is a clear error, and the retired
// "microvm" backend is a migration error — never a silent fallback.
func TestBuildImageNoMsb(t *testing.T) {
	newProject(t)
	t.Setenv("SANDBOXER_MSB", "/nonexistent/msb-not-here")
	if code, _, errs := run("image", "build"); code != 1 || !strings.Contains(errs, "msb") {
		t.Errorf("build without msb = (%d, %q)", code, errs)
	}
	fakeMsbOnPath(t)
	if code, _, errs := run("image", "build", "--backend", "microvm"); code != 1 ||
		!strings.Contains(errs, "microvm backend was removed") {
		t.Errorf("build --backend microvm = (%d, %q), want the smolvm-removal migration error", code, errs)
	}
}

// TestImagePull: `image pull` shells out to `msb pull -f <ref>` with the image
// resolved like the other image commands (SANDBOXER_IMAGE over the prebuilt
// default), streaming through and reporting what was pulled; a profile with
// image customization is refused — its variant is never published.
func TestImagePull(t *testing.T) {
	requireExec(t, "sh")
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	dir := t.TempDir()
	bin := filepath.Join(dir, "msb")
	log := filepath.Join(dir, "log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$FAKE_PULL_LOG\"\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SANDBOXER_MSB", bin)
	t.Setenv("FAKE_PULL_LOG", log)

	code, out, errs := run("image", "pull")
	if code != 0 {
		t.Fatalf("image pull = %d, %s", code, errs)
	}
	data, err := os.ReadFile(log)
	if err != nil || !strings.Contains(string(data), "pull -f "+config.DefaultImage) {
		t.Errorf("msb argv log = %q, %v; want `pull -f %s`", data, err, config.DefaultImage)
	}
	if !strings.Contains(out, config.DefaultImage) {
		t.Errorf("pull output = %q, want the pulled ref", out)
	}

	// SANDBOXER_IMAGE resolves exactly as for build/rm.
	t.Setenv("SANDBOXER_IMAGE", "example.com/custom:1")
	if code, _, errs := run("image", "pull"); code != 0 {
		t.Fatalf("image pull (SANDBOXER_IMAGE) = %d, %s", code, errs)
	}
	if data, _ := os.ReadFile(log); !strings.Contains(string(data), "pull -f example.com/custom:1") {
		t.Errorf("msb argv log = %q, want the SANDBOXER_IMAGE ref", data)
	}
	t.Setenv("SANDBOXER_IMAGE", "")

	// A customized profile's variant cannot be pulled — refuse with the build
	// hint instead of asking msb for an image that cannot exist.
	cfg := filepath.Join(t.TempDir(), "img.nix")
	if err := os.WriteFile(cfg, []byte("{ name = \"feat\"; image.packages = [ \"ripgrep\" ]; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("image", "pull", "-f", cfg); code != 1 ||
		!strings.Contains(errs, "sandboxer image build") {
		t.Errorf("pull of a customized profile = (%d, %q), want a refusal with the build hint", code, errs)
	}

	// A ref-only profile is exactly as pullable as the default: pull fetches
	// ITS reference, not the configured default.
	refCfg := filepath.Join(t.TempDir(), "ref.nix")
	if err := os.WriteFile(refCfg, []byte("{ name = \"feat\"; image.ref = \"ghcr.io/me/mine:7\"; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, errs := run("image", "pull", "-f", refCfg); code != 0 || !strings.Contains(out, "ghcr.io/me/mine:7") {
		t.Errorf("pull of a ref profile = (%d, %q, %q), want its own ref pulled", code, out, errs)
	}
	if data, _ := os.ReadFile(log); !strings.Contains(string(data), "pull -f ghcr.io/me/mine:7") {
		t.Errorf("msb argv log = %q, want the profile's ref", data)
	}

	// image build refuses a ref-only profile — its refresh story is the pull.
	if code, _, errs := run("image", "build", "-f", refCfg); code != 1 ||
		!strings.Contains(errs, "sandboxer image pull") {
		t.Errorf("build of a ref profile = (%d, %q), want a refusal with the pull hint", code, errs)
	}

	// image rm respects the profile's ref too.
	if code, out, _ := run("image", "rm", "-f", refCfg); code != 0 || !strings.Contains(out, "ghcr.io/me/mine:7") {
		t.Errorf("rm of a ref profile = (%d, %q), want its own ref removed", code, out)
	}
}

// TestRemoveImageMsb: `image rm` reaches the store via the microsandbox engine
// identity (RemoveImage dispatches on it).
func TestRemoveImageMsb(t *testing.T) {
	newProject(t)
	fakeMsbOnPath(t)

	var gotEngine string
	old := backendRemoveImage
	defer func() { backendRemoveImage = old }()
	backendRemoveImage = func(engine, _ string) error {
		gotEngine = engine
		return nil
	}
	if code, _, errs := run("image", "rm"); code != 0 {
		t.Fatalf("image rm = %d %s", code, errs)
	}
	if gotEngine != "microsandbox" {
		t.Errorf("rm engine = %q, want microsandbox", gotEngine)
	}
}

// TestImageBackendPrecedence: backend is flag > profile > default, resolved
// to the runner's engine identity.
func TestImageBackendPrecedence(t *testing.T) {
	fakeMsbOnPath(t)
	d := config.Defaults{Backend: "microsandbox"}

	if eng, err := imageBackend("microsandbox", nil, d); err != nil || eng != "microsandbox" {
		t.Errorf("flag microsandbox → (%q, %v)", eng, err)
	}
	if eng, err := imageBackend("", &config.Profile{Backend: "microsandbox"}, d); err != nil || eng != "microsandbox" {
		t.Errorf("profile microsandbox → (%q, %v)", eng, err)
	}
	// A container-era default is an error, not a silent engine.
	if _, err := imageBackend("", nil, config.Defaults{Backend: "docker"}); err == nil {
		t.Error("a docker default must error after the container-backend removal")
	}
	// The retired smolvm backend errors with the migration hint.
	if _, err := imageBackend("microvm", nil, d); err == nil {
		t.Error("a microvm flag must error after the smolvm removal")
	}
}
