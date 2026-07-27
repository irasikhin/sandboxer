package backend

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
)

// TestVMImageStore pins the store round trip: store a tar, find it, read its id
// (the sha256 sidecar), and remove it — all through the engine-neutral image
// dispatch (engine == smolvm).
func TestVMImageStore(t *testing.T) {
	t.Setenv("SANDBOXER_STATE", t.TempDir())

	// A stand-in "image tar".
	src := filepath.Join(t.TempDir(), "built.tar")
	if err := os.WriteFile(src, []byte("IMAGE-BYTES"), 0o600); err != nil {
		t.Fatal(err)
	}
	name := "sandboxer-toolbox:latest"

	if ImageExists(smolvmEngine, name) {
		t.Fatal("image present before store")
	}
	if err := vmStoreImage(name, src); err != nil {
		t.Fatalf("vmStoreImage: %v", err)
	}
	if !ImageExists(smolvmEngine, name) {
		t.Error("image missing after store")
	}
	// The id is the sha256 of the tar, and it is cached in a sidecar (a second
	// read must not depend on the tar).
	id := ImageID(smolvmEngine, name)
	if len(id) != 64 {
		t.Errorf("ImageID = %q, want a 64-hex digest", id)
	}
	if _, err := os.Stat(vmImagePath(name) + ".sha256"); err != nil {
		t.Errorf("sha256 sidecar not written: %v", err)
	}

	if err := RemoveImage(smolvmEngine, name); err != nil {
		t.Fatalf("RemoveImage: %v", err)
	}
	if ImageExists(smolvmEngine, name) {
		t.Error("image present after remove")
	}
	// Idempotent remove.
	if err := RemoveImage(smolvmEngine, name); err != nil {
		t.Errorf("second RemoveImage: %v", err)
	}
}

// TestVMImageIDRecomputes pins that a present tar with no sidecar still yields an
// id (computed once, then cached).
func TestVMImageIDRecomputes(t *testing.T) {
	t.Setenv("SANDBOXER_STATE", t.TempDir())
	name := "sandboxer-toolbox:latest"
	p := vmImagePath(name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("no-sidecar"), 0o600); err != nil {
		t.Fatal(err)
	}
	if id := vmImageID(name); len(id) != 64 {
		t.Errorf("vmImageID with no sidecar = %q, want 64-hex", id)
	}
	if _, err := os.Stat(p + ".sha256"); err != nil {
		t.Error("id was not cached to a sidecar")
	}
	// A missing image has no id.
	if id := vmImageID("nope:latest"); id != "" {
		t.Errorf("missing image id = %q, want empty", id)
	}
}

// TestHostProxyIsLoopback pins the loopback-proxy detection that decides whether
// the container image build needs --network=host to reach a loopback-bound proxy.
func TestHostProxyIsLoopback(t *testing.T) {
	for _, n := range []string{"http_proxy", "https_proxy", "all_proxy", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"} {
		t.Setenv(n, "")
	}
	if hostProxyIsLoopback() {
		t.Error("no proxy → not loopback")
	}
	for _, v := range []string{"http://127.0.0.1:8888", "http://localhost:3128", "http://[::1]:8080", "http://127.1.2.3:9"} {
		t.Setenv("https_proxy", v)
		if !hostProxyIsLoopback() {
			t.Errorf("%s should be detected as loopback", v)
		}
	}
	t.Setenv("https_proxy", "http://proxy.lan:3128")
	if hostProxyIsLoopback() {
		t.Error("a LAN proxy is not loopback")
	}
}

// TestVMEnsureImagePublicRef pins that a custom public image is handed to smolvm
// untouched (no local build).
func TestVMEnsureImagePublicRef(t *testing.T) {
	t.Setenv("SANDBOXER_STATE", t.TempDir())
	ref, err := vmEnsureImage(RunOpts{Image: "docker.io/library/alpine:3"})
	if err != nil {
		t.Fatalf("vmEnsureImage: %v", err)
	}
	if ref != "docker.io/library/alpine:3" {
		t.Errorf("public ref = %q, want it passed through", ref)
	}
}

// TestVMEnsureImageBuilds pins the auto-build path: a missing toolbox image is
// built into the store (build stubbed) and its tar path is returned.
func TestVMEnsureImageBuilds(t *testing.T) {
	t.Setenv("SANDBOXER_STATE", t.TempDir())
	restore := vmBuildImageToStore
	vmBuildImageToStore = func(o RunOpts) error {
		p := vmImagePath(o.Image)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return err
		}
		return os.WriteFile(p, []byte("built"), 0o600)
	}
	t.Cleanup(func() { vmBuildImageToStore = restore })

	o := RunOpts{Image: config.DefaultImage, Stderr: &bytes.Buffer{}}
	ref, err := vmEnsureImage(o)
	if err != nil {
		t.Fatalf("vmEnsureImage: %v", err)
	}
	if ref != vmImagePath(config.DefaultImage) {
		t.Errorf("ensured ref = %q, want the store tar path", ref)
	}

	// With autobuild disabled, a missing image is a clear error.
	t.Setenv("SANDBOXER_STATE", t.TempDir()) // fresh, empty store
	t.Setenv("SANDBOXER_NO_AUTOBUILD", "1")
	if _, err := vmEnsureImage(o); err == nil || !strings.Contains(err.Error(), "image build") {
		t.Errorf("no-autobuild ensure = %v, want a build hint", err)
	}
}
