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

// fakeSmolvmOnPath points SANDBOXER_SMOLVM at a dummy executable so
// ResolveEngine("microvm") resolves without a real smolvm.
func fakeSmolvmOnPath(t *testing.T) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "smolvm")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SANDBOXER_SMOLVM", bin)
}

// TestBuildImageMicrovmRoutesToVMBuild: `image build --backend microvm` builds
// in a microVM (the store path), never the container engine's image store. The
// pin resolver still runs on a container engine (here the pin-serving fake
// podman, forced via SANDBOXER_ENGINE), and the resolved revs reach the VM
// build's spec.
func TestBuildImageMicrovmRoutesToVMBuild(t *testing.T) {
	requireExec(t, "sh")
	newProject(t)
	fakeSmolvmOnPath(t)
	rev := strings.Repeat("b", 40)
	pinPodman(t, rev)
	t.Setenv("SANDBOXER_ENGINE", "podman")

	var vmImage string
	var vmSpec toolbox.Spec
	oldVM, oldC := backendBuildVMImage, toolboxBuild
	defer func() { backendBuildVMImage, toolboxBuild = oldVM, oldC }()
	backendBuildVMImage = func(_, image string, spec toolbox.Spec, _ io.Writer) error {
		vmImage = image
		vmSpec = spec
		return nil
	}
	toolboxBuild = func(toolbox.BuildOpts) error {
		t.Fatal("the container build must not run for backend=microvm")
		return nil
	}

	if code, _, errs := run("image", "build", "--backend", "microvm"); code != 0 {
		t.Fatalf("build --backend microvm = %d %s", code, errs)
	}
	if vmImage != config.DefaultImage {
		t.Errorf("vm build image = %q, want %q", vmImage, config.DefaultImage)
	}
	if vmSpec.NixpkgsRev != rev || vmSpec.LLMAgentsRev != rev {
		t.Errorf("vm build spec revs = %q/%q, want the resolved %s", vmSpec.NixpkgsRev, vmSpec.LLMAgentsRev, rev)
	}
}

// TestBuildImageMicrovmNoResolver: with no container engine at all the default
// refresh degrades to the stamped pins (with a warning) instead of failing —
// and a cold stamp is what fails, closed.
func TestBuildImageMicrovmNoResolver(t *testing.T) {
	newProject(t)
	fakeSmolvmOnPath(t)
	t.Setenv("SANDBOXER_ENGINE", "")
	t.Setenv("PATH", t.TempDir()) // no docker/podman anywhere

	oldVM := backendBuildVMImage
	defer func() { backendBuildVMImage = oldVM }()
	backendBuildVMImage = func(_, _ string, _ toolbox.Spec, _ io.Writer) error { return nil }

	// Cold pins cache → fail-closed with image-build guidance.
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if code, _, errs := run("image", "build", "--backend", "microvm"); code != 1 ||
		!strings.Contains(errs, "image build") {
		t.Errorf("cold cache without a resolver = (%d, %q), want fail-closed guidance", code, errs)
	}

	// Warm stamp → the build proceeds from it, saying the refresh was skipped.
	warmPins(t, strings.Repeat("c", 40))
	if code, _, errs := run("image", "build", "--backend", "microvm"); code != 0 ||
		!strings.Contains(errs, "stamped pins") {
		t.Errorf("warm stamp without a resolver = (%d, %q), want a build from the stamp", code, errs)
	}
}

// TestBuildImageMicrovmVariant: a profile with image customization builds its
// content-addressed variant tag in the microVM.
func TestBuildImageMicrovmVariant(t *testing.T) {
	requireExec(t, "sh")
	newProject(t)
	fakeSmolvmOnPath(t)
	pinPodman(t, strings.Repeat("b", 40))
	t.Setenv("SANDBOXER_ENGINE", "podman")

	var vmImage string
	oldVM := backendBuildVMImage
	defer func() { backendBuildVMImage = oldVM }()
	backendBuildVMImage = func(_, image string, _ toolbox.Spec, _ io.Writer) error {
		vmImage = image
		return nil
	}

	cfg := filepath.Join(t.TempDir(), "img.nix")
	if err := os.WriteFile(cfg, []byte("{ name = \"feat\"; backend = \"microvm\"; image.packages = [ \"ripgrep\" ]; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// No --backend flag: the backend comes from the profile.
	if code, _, errs := run("image", "build", "-f", cfg); code != 0 {
		t.Fatalf("build -f (microvm profile) = %d %s", code, errs)
	}
	if !strings.Contains(vmImage, "sandboxer-toolbox:var-") {
		t.Errorf("vm build image = %q, want a var- variant", vmImage)
	}
}

// TestBuildImageMicrovmNoSmolvm: a missing smolvm is a clear error, not a
// silent fall back to a container engine.
func TestBuildImageMicrovmNoSmolvm(t *testing.T) {
	newProject(t)
	t.Setenv("SANDBOXER_SMOLVM", "/nonexistent/smolvm-not-here")
	if code, _, errs := run("image", "build", "--backend", "microvm"); code != 1 || !strings.Contains(errs, "smolvm") {
		t.Errorf("build --backend microvm without smolvm = (%d, %q)", code, errs)
	}
}

// TestRemoveImageMicrovm: `image rm --backend microvm` reaches the store via the
// smolvm-identity engine (RemoveImage dispatches on it).
func TestRemoveImageMicrovm(t *testing.T) {
	newProject(t)
	fakeSmolvmOnPath(t)

	var gotEngine string
	old := backendRemoveImage
	defer func() { backendRemoveImage = old }()
	backendRemoveImage = func(engine, _ string) error {
		gotEngine = engine
		return nil
	}
	if code, _, errs := run("image", "rm", "--backend", "microvm"); code != 0 {
		t.Fatalf("rm --backend microvm = %d %s", code, errs)
	}
	if gotEngine != "smolvm" {
		t.Errorf("rm engine = %q, want smolvm", gotEngine)
	}
}

// TestImageBackendPrecedence: backend is flag > profile > default.
func TestImageBackendPrecedence(t *testing.T) {
	fakeSmolvmOnPath(t)
	d := config.Defaults{Backend: "docker"}

	if be, eng, err := imageBackend("microvm", "", nil, d); err != nil || be != "microvm" || eng != "smolvm" {
		t.Errorf("flag microvm → (%q, %q, %v)", be, eng, err)
	}
	if be, _, err := imageBackend("", "", &config.Profile{Backend: "microvm"}, d); err != nil || be != "microvm" {
		t.Errorf("profile microvm → (%q, %v)", be, err)
	}
}
