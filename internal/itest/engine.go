//go:build integration

package itest

import (
	"os"
	"os/exec"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/toolbox"
)

// Engine returns a usable container engine ("podman" preferred, then "docker"),
// or skips the test. "Usable" means the binary is on PATH AND a throwaway
// `network create`/`network rm` round-trips — proving the daemon (docker) or the
// rootless stack (podman + pasta/netavark) actually works, not merely that the
// binary exists. This keeps the suite green on partial rootless setups by
// skipping rather than failing. Set SANDBOXER_ITEST_ENGINE to pin a specific
// engine (e.g. when both are installed but only one has the images pulled).
func Engine(t *testing.T) string {
	t.Helper()
	candidates := []string{"podman", "docker"}
	if forced := os.Getenv("SANDBOXER_ITEST_ENGINE"); forced != "" {
		candidates = []string{forced}
	}
	for _, eng := range candidates {
		if _, err := exec.LookPath(eng); err != nil {
			continue
		}
		name := Slug("sbx-itest-canary")
		if err := exec.Command(eng, "network", "create", name).Run(); err != nil {
			continue
		}
		_ = exec.Command(eng, "network", "rm", name).Run()
		return eng
	}
	t.Skip("no usable container engine (podman/docker) — skipping real integration test")
	return ""
}

// CleanupContainer registers a best-effort `rm -f` in t.Cleanup so the container
// is reaped even if the test panics or fails an assertion. Names are unique per
// process (itest.Slug), so no up-front removal is needed; cleanups run LIFO, so
// a container registered after its networks is torn down first.
func CleanupContainer(t *testing.T, engine, name string) {
	t.Helper()
	t.Cleanup(func() { _ = exec.Command(engine, "rm", "-f", name).Run() })
}

// CleanupNetwork is CleanupContainer's network analogue.
func CleanupNetwork(t *testing.T, engine, name string) {
	t.Helper()
	t.Cleanup(func() { _ = exec.Command(engine, "network", "rm", name).Run() })
}

// SmokeImage returns a small, ALREADY-PULLED image (alpine/busybox) that ships
// sh + wget + httpd, or skips. It never pulls, so the suite stays offline-safe
// and never blocks on a registry; pull one beforehand (e.g. `docker pull alpine`).
func SmokeImage(t *testing.T, engine string) string {
	t.Helper()
	for _, img := range []string{"alpine:latest", "alpine", "busybox:latest", "busybox"} {
		if exec.Command(engine, "image", "inspect", img).Run() == nil {
			return img
		}
	}
	t.Skipf("no smoke image present for %s (pull one: %s pull alpine)", engine, engine)
	return ""
}

// EnsureToolboxImage guarantees the toolbox image (which bakes in the sandboxer
// binary the egress sidecar runs) is present, else skips. It builds the image
// only when SANDBOXER_ITEST_BUILD_IMAGE=1, since the build drives a multi-minute
// nix-in-container step that is out of scope for a normal test run.
func EnsureToolboxImage(t *testing.T, engine string) string {
	t.Helper()
	img := config.DefaultImage
	if exec.Command(engine, "image", "inspect", img).Run() == nil {
		return img
	}
	if os.Getenv("SANDBOXER_ITEST_BUILD_IMAGE") != "1" {
		t.Skipf("toolbox image %q absent — build it (sandboxer image build) or set SANDBOXER_ITEST_BUILD_IMAGE=1", img)
	}
	if err := toolbox.BuildImage(toolbox.BuildOpts{Engine: engine, Image: img, Stdout: os.Stderr, Stderr: os.Stderr}); err != nil {
		t.Fatalf("build toolbox image: %v", err)
	}
	return img
}

// EnsureProxyImage guarantees the egress squid proxy image (config.ProxyImage,
// the sidecar the container backend runs) is present, else skips. Like
// EnsureToolboxImage it builds only when SANDBOXER_ITEST_BUILD_IMAGE=1 — and one
// toolbox.BuildImage run produces BOTH the toolbox image and the proxy image, so
// a suite that needs both pays the multi-minute nix build once. It honours the
// SANDBOXER_PROXY_IMAGE override via config.ProxyImage, so a test that points
// that at a bogus tag (to force a sidecar failure) must NOT call this.
func EnsureProxyImage(t *testing.T, engine string) string {
	t.Helper()
	img := config.ProxyImage()
	if exec.Command(engine, "image", "inspect", img).Run() == nil {
		return img
	}
	if os.Getenv("SANDBOXER_ITEST_BUILD_IMAGE") != "1" {
		t.Skipf("proxy image %q absent — build it (sandboxer image build) or set SANDBOXER_ITEST_BUILD_IMAGE=1", img)
	}
	// BuildImage builds and loads the toolbox image and the squid proxy image in
	// one nix run; the toolbox image is a harmless by-product here.
	if err := toolbox.BuildImage(toolbox.BuildOpts{Engine: engine, Image: config.DefaultImage, Stdout: os.Stderr, Stderr: os.Stderr}); err != nil {
		t.Fatalf("build proxy image: %v", err)
	}
	return img
}
