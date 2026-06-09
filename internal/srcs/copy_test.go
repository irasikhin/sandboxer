package srcs

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCopyEntryTree exercises copyEntry/copyTree across all three node kinds —
// a regular file, a nested directory, and a symlink (preserved, not followed).
func TestCopyEntryTree(t *testing.T) {
	// given: a source tree with a file, a nested dir+file, and a symlink
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.txt", filepath.Join(src, "link")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	// when: copied into a fresh destination
	dst := filepath.Join(t.TempDir(), "out")
	if err := copyEntry(src, dst); err != nil {
		t.Fatalf("copyEntry: %v", err)
	}

	// then: contents and structure are reproduced
	if b, err := os.ReadFile(filepath.Join(dst, "a.txt")); err != nil || string(b) != "hello" {
		t.Errorf("a.txt = %q, %v; want \"hello\"", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "sub", "b.txt")); err != nil || string(b) != "world" {
		t.Errorf("sub/b.txt = %q, %v; want \"world\"", b, err)
	}
	fi, err := os.Lstat(filepath.Join(dst, "link"))
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("link not preserved as a symlink: mode=%v err=%v", fi.Mode(), err)
	}
}

// TestSearchDepMissingRoot exercises the unreadable-root path in walk: a root
// that does not exist yields no matches (and does not panic).
func TestSearchDepMissingRoot(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if got := searchDep([]string{missing}, "lib"); len(got) != 0 {
		t.Errorf("searchDep(missing root) = %v, want none", got)
	}
}
