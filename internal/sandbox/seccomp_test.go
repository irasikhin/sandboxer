package sandbox

import (
	"os"
	"strings"
	"testing"
)

// TestEnsureSeccompProfile: the write lands under _meta with the
// content-addressed name, the pure path twin agrees (show/compose hash the
// same argv enter builds), and a repeat call is a no-op.
func TestEnsureSeccompProfile(t *testing.T) {
	base, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path, err := base.EnsureSeccompProfile()
	if err != nil {
		t.Fatal(err)
	}
	pure, err := base.SeccompProfilePath()
	if err != nil || pure != path {
		t.Errorf("SeccompProfilePath = (%q, %v), want the written path %q", pure, err, path)
	}
	if !strings.Contains(path, "_meta") || !strings.Contains(path, "seccomp-") {
		t.Errorf("profile path %q, want a content-addressed file under _meta", path)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		t.Errorf("written profile = (%v, %v), want a non-empty file", info, err)
	}
	again, err := base.EnsureSeccompProfile()
	if err != nil || again != path {
		t.Errorf("second ensure = (%q, %v), want the same path", again, err)
	}
}
