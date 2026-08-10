package backend

import (
	"os"
	"path/filepath"
	"testing"
)

// TestImageExists pins the VM-store dispatch: with a state root holding no
// tars and no msb cache, a bogus reference reports absent on both engine
// identities — and a tar dropped into the store flips the smolvm answer.
func TestImageExists(t *testing.T) {
	// given an empty image store
	t.Setenv("SANDBOXER_STATE", t.TempDir())
	t.Setenv("SANDBOXER_MSB", filepath.Join(t.TempDir(), "no-msb"))

	// then: absent everywhere
	if ImageExists(smolvmEngine, "sandboxer-definitely-absent:v0") {
		t.Error("ImageExists(smolvm) reported a bogus image as present")
	}
	if ImageExists(msbEngine, "sandboxer-definitely-absent:v0") {
		t.Error("ImageExists(microsandbox) reported a bogus image as present")
	}

	// when a tar lands in the shared store
	p := vmImagePath("present:v1")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("tar"), 0o600); err != nil {
		t.Fatal(err)
	}
	// then the smolvm identity sees it
	if !ImageExists(smolvmEngine, "present:v1") {
		t.Error("ImageExists(smolvm) missed a stored tar")
	}
}
