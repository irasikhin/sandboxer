package backend

import (
	"os"
	"path/filepath"
	"testing"
)

// TestVMImageStore pins the build-tar store round trip: store a tar, find it,
// read its id (the sha256 sidecar), and remove it through the exported
// RemoveImage — which must drop the tar even when msb never cached the ref.
func TestVMImageStore(t *testing.T) {
	setupFakeMSB(t) // its `image` verb exits non-zero: nothing is msb-cached

	// A stand-in "image tar".
	src := filepath.Join(t.TempDir(), "built.tar")
	if err := os.WriteFile(src, []byte("IMAGE-BYTES"), 0o600); err != nil {
		t.Fatal(err)
	}
	name := "sandboxer-toolbox:latest"

	if vmImageExists(name) {
		t.Fatal("image present before store")
	}
	if err := vmStoreImage(name, src); err != nil {
		t.Fatalf("vmStoreImage: %v", err)
	}
	if !vmImageExists(name) {
		t.Error("image missing after store")
	}
	// The id is the sha256 of the tar, cached in a sidecar, and it is what the
	// exported ImageID reports for a store-built image (the freshness
	// authority — see msbImageID).
	id := ImageID(msbEngine, name)
	if len(id) != 64 {
		t.Errorf("ImageID = %q, want a 64-hex digest", id)
	}
	if _, err := os.Stat(vmImagePath(name) + ".sha256"); err != nil {
		t.Errorf("sha256 sidecar not written: %v", err)
	}

	if err := RemoveImage(msbEngine, name); err != nil {
		t.Fatalf("RemoveImage: %v", err)
	}
	if vmImageExists(name) {
		t.Error("image present after remove")
	}
	// Idempotent remove.
	if err := RemoveImage(msbEngine, name); err != nil {
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
