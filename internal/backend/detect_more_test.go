package backend

import (
	"os/exec"
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

// TestEngineLabel covers EngineLabel's three branches directly (it was only
// asserted indirectly inside TestResolveEngine): native passthrough, the
// explicit-engine win, and the no-engine-installed fallback to the configured
// backend.
func TestEngineLabel(t *testing.T) {
	if got := EngineLabel("native", config.Defaults{}); got != "native" {
		t.Errorf("EngineLabel(native) = %q, want native", got)
	}
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
