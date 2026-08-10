package backend

import (
	"path/filepath"
	"testing"
)

// TestImageExists pins the store dispatch: ImageExists asks msb's OWN image
// store (what a create boots), so with no msb reachable a bogus reference —
// and even a tar sitting in the shared build store — reports absent; the
// import at create time is what makes a built tar bootable.
func TestImageExists(t *testing.T) {
	// given an empty image store and no msb
	t.Setenv("SANDBOXER_STATE", t.TempDir())
	t.Setenv("SANDBOXER_MSB", filepath.Join(t.TempDir(), "no-msb"))

	// then: absent
	if ImageExists(msbEngine, "sandboxer-definitely-absent:v0") {
		t.Error("ImageExists reported a bogus image as present")
	}

	// when msb's inventory knows the ref
	restore := msbImageInspect
	msbImageInspect = func(ref string) string {
		if ref == "present:v1" {
			return "deadbeef"
		}
		return ""
	}
	t.Cleanup(func() { msbImageInspect = restore })
	// then it reads as present
	if !ImageExists(msbEngine, "present:v1") {
		t.Error("ImageExists missed an msb-cached image")
	}
}
