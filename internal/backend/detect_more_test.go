package backend

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
)

// TestImageExists exercises the real `<engine> image inspect` path. Production
// code reaches it only through the imageExists seam (which TestEnsureImage
// overrides), so the exported function body is otherwise never executed. A bogus
// reference must report absent; this also works without a running daemon (the
// inspect simply errors, which is the same "absent" answer).
func TestImageExists(t *testing.T) {
	engine := ""
	for _, e := range []string{"docker", "podman"} {
		if _, err := exec.LookPath(e); err == nil {
			engine = e
			break
		}
	}
	if engine == "" {
		t.Skip("no container engine for ImageExists")
	}
	if ImageExists(engine, "sandboxer-definitely-absent:v0") {
		t.Error("ImageExists reported a bogus image as present")
	}
}

// TestInstalledEngines pins the sweep enumeration: the SANDBOXER_ENGINE
// override alone when set, otherwise exactly the engines found on PATH —
// podman first, then docker — and nil on an engine-less host.
func TestInstalledEngines(t *testing.T) {
	// The override is returned verbatim, even when nothing is installed.
	t.Setenv("PATH", t.TempDir())
	if got := InstalledEngines(config.Defaults{Engine: "customengine"}); len(got) != 1 || got[0] != "customengine" {
		t.Errorf("InstalledEngines with override = %v, want [customengine]", got)
	}
	// Engine-less host: nothing to sweep.
	if got := InstalledEngines(config.Defaults{}); got != nil {
		t.Errorf("engine-less InstalledEngines = %v, want nil", got)
	}
	// Both installed: both reported, podman first.
	bin := t.TempDir()
	for _, e := range []string{"podman", "docker"} {
		if err := os.WriteFile(filepath.Join(bin, e), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	if got := InstalledEngines(config.Defaults{}); len(got) != 2 || got[0] != "podman" || got[1] != "docker" {
		t.Errorf("InstalledEngines = %v, want [podman docker]", got)
	}
	// Only docker installed: podman is not invented.
	if err := os.Remove(filepath.Join(bin, "podman")); err != nil {
		t.Fatal(err)
	}
	if got := InstalledEngines(config.Defaults{}); len(got) != 1 || got[0] != "docker" {
		t.Errorf("docker-only InstalledEngines = %v, want [docker]", got)
	}
}

// TestEngineLabel covers EngineLabel's branches directly (it was only asserted
// indirectly inside TestResolveEngine): the explicit-engine win, and the
// no-engine-installed fallback to the configured backend.
func TestEngineLabel(t *testing.T) {
	// SANDBOXER_ENGINE (Defaults.Engine) wins outright, so the label echoes it.
	if got := EngineLabel("podman", config.Defaults{Engine: "customengine"}); got != "customengine" {
		t.Errorf("EngineLabel with explicit engine = %q, want customengine", got)
	}
	// With neither podman nor docker resolvable, the configured backend is
	// echoed unchanged so the banner still names it.
	t.Setenv("PATH", t.TempDir()) // empty dir: no engine binaries found
	if got := EngineLabel("podman", config.Defaults{}); got != "podman" {
		t.Errorf("EngineLabel fallback = %q, want podman", got)
	}
}
