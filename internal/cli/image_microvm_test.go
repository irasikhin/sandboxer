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
// in a microVM (the store path), never the container engine.
func TestBuildImageMicrovmRoutesToVMBuild(t *testing.T) {
	newProject(t)
	fakeSmolvmOnPath(t)

	var vmImage string
	oldVM, oldC := backendBuildVMImage, toolboxBuild
	defer func() { backendBuildVMImage, toolboxBuild = oldVM, oldC }()
	backendBuildVMImage = func(_, image string, _ toolbox.Spec, _ io.Writer) error {
		vmImage = image
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
}

// TestBuildImageMicrovmVariant: a profile with image customization builds its
// content-addressed variant tag in the microVM.
func TestBuildImageMicrovmVariant(t *testing.T) {
	newProject(t)
	fakeSmolvmOnPath(t)

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
