//go:build unix

package sandbox

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFileIdentityRealFile: a real file yields a "dev:inode" identity, and it is
// stable for the same inode.
func TestFileIdentityRealFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	id := fileIdentity(fi)
	if !strings.Contains(id, ":") {
		t.Errorf("fileIdentity = %q, want a dev:inode form", id)
	}
	fi2, _ := os.Stat(p)
	if again := fileIdentity(fi2); again != id {
		t.Errorf("fileIdentity not stable for the same inode: %q vs %q", id, again)
	}
}

// fakeInfo is an fs.FileInfo whose Sys() is not a *syscall.Stat_t, exercising
// fileIdentity's platform fallback (unreachable via os.Stat on unix).
type fakeInfo struct {
	size int64
	mod  time.Time
}

func (f fakeInfo) Name() string       { return "fake" }
func (f fakeInfo) Size() int64        { return f.size }
func (f fakeInfo) Mode() fs.FileMode  { return 0 }
func (f fakeInfo) ModTime() time.Time { return f.mod }
func (f fakeInfo) IsDir() bool        { return false }
func (f fakeInfo) Sys() any           { return nil }

// TestFileIdentityFallback: when the platform stat is not the expected shape,
// fileIdentity falls back to a size/mtime identity rather than panicking, and
// that identity still changes when the file does.
func TestFileIdentityFallback(t *testing.T) {
	when := time.Unix(1_700_000_000, 0)
	a := fileIdentity(fakeInfo{size: 10, mod: when})
	if !strings.HasPrefix(a, "sz") {
		t.Errorf("fallback identity = %q, want the sz/mt form", a)
	}
	if b := fileIdentity(fakeInfo{size: 11, mod: when}); b == a {
		t.Error("fallback identity did not change with the size")
	}
	if b := fileIdentity(fakeInfo{size: 10, mod: when.Add(time.Second)}); b == a {
		t.Error("fallback identity did not change with the mtime")
	}
}
