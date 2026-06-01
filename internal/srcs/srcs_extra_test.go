package srcs

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDepsSearchAndLayout(t *testing.T) {
	// Two roots; a dep matched by path SUFFIX lands flat at <sandbox>/<dep>.
	base := t.TempDir()
	rootA := filepath.Join(base, "a")
	rootB := filepath.Join(base, "b")
	writeFile(t, filepath.Join(rootA, "src", "lib", "util.go"), "deep\n") // matches src/lib/util.go
	writeFile(t, filepath.Join(rootA, "other", "util.go"), "wrong\n")     // same leaf, wrong suffix
	writeFile(t, filepath.Join(rootB, "config.yaml"), "cfg\n")
	writeFile(t, filepath.Join(rootA, ".git", "util.go"), "git\n") // hidden/.git skipped

	sandbox := filepath.Join(base, "sandbox")
	manifest := filepath.Join(base, "m.json")
	profile := Profile{
		Roots: []string{rootA, rootB},
		Deps:  []string{"src/lib/util.go", "config.yaml", "missing.txt"},
	}
	pf := writeProfile(t, base, profile)

	var out bytes.Buffer
	if err := CopyIn(&out, pf, sandbox, manifest, false); err != nil {
		t.Fatalf("CopyIn: %v", err)
	}
	// Suffix match → flat layout at <sandbox>/<dep>.
	if got := readFile(t, filepath.Join(sandbox, "src", "lib", "util.go")); got != "deep\n" {
		t.Errorf("dep landed wrong: %q", got)
	}
	if got := readFile(t, filepath.Join(sandbox, "config.yaml")); got != "cfg\n" {
		t.Errorf("dep from second root: %q", got)
	}
	// Not-found dep is reported, .git is skipped (so no util.go from .git won).
	if !strings.Contains(out.String(), "SKIP") || !strings.Contains(out.String(), "missing.txt") {
		t.Errorf("expected SKIP for missing dep, got: %q", out.String())
	}
}

func TestDepsMultiMatchAndDefaultRoot(t *testing.T) {
	// No roots → search cwd; a dep with two suffix matches → WARN + first.
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "p", "x.txt"), "one")
	writeFile(t, filepath.Join(dir, "q", "x.txt"), "two")
	t.Chdir(dir)

	sandbox := filepath.Join(t.TempDir(), "sb")
	manifest := filepath.Join(t.TempDir(), "m.json")
	pf := writeProfile(t, dir, Profile{Deps: []string{"x.txt"}}) // roots empty → cwd

	var out bytes.Buffer
	if err := CopyIn(&out, pf, sandbox, manifest, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "WARN") {
		t.Errorf("expected WARN for multiple matches, got: %q", out.String())
	}
	if !exists(filepath.Join(sandbox, "x.txt")) {
		t.Error("dep not copied from the default (cwd) root")
	}
}

func TestCopyEntry(t *testing.T) {
	// Missing src errors.
	if err := copyEntry(filepath.Join(t.TempDir(), "nope"), filepath.Join(t.TempDir(), "dst")); err == nil {
		t.Error("copyEntry of a missing src should error")
	}
	// A directory is copied recursively, and dst is replaced wholesale: a stale
	// file already at dst must be gone afterwards (like depsync rmtree+copytree).
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a", "b.txt"), "hi")
	dst := filepath.Join(t.TempDir(), "out")
	writeFile(t, filepath.Join(dst, "stale.txt"), "old")
	if err := copyEntry(src, dst); err != nil {
		t.Fatalf("copyEntry dir: %v", err)
	}
	if !exists(filepath.Join(dst, "a", "b.txt")) {
		t.Error("copyEntry did not recreate nested file")
	}
	if exists(filepath.Join(dst, "stale.txt")) {
		t.Error("copyEntry must remove the destination before copying")
	}
	// copyEntry under a file path fails (MkdirAll on a file parent).
	blocker := filepath.Join(t.TempDir(), "blocker")
	writeFile(t, blocker, "x")
	if err := copyEntry(src, filepath.Join(blocker, "child")); err == nil {
		t.Error("copyEntry under a file path should error")
	}
}

func TestCopyEntrySymlink(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "target.txt"), "data\n")
	link := filepath.Join(dir, "link")
	if err := os.Symlink("target.txt", link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "out", "link")
	if err := copyEntry(link, dst); err != nil {
		t.Fatalf("copyEntry symlink: %v", err)
	}
	fi, err := os.Lstat(dst)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink not preserved: mode=%v err=%v", fi.Mode(), err)
	}
	if got, _ := os.Readlink(dst); got != "target.txt" {
		t.Errorf("symlink target = %q, want target.txt", got)
	}
}

func TestManifestIO(t *testing.T) {
	if readManifest(filepath.Join(t.TempDir(), "missing")) != nil {
		t.Error("missing manifest should read as nil")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	writeFile(t, bad, "not json")
	if readManifest(bad) != nil {
		t.Error("garbage manifest should read as nil")
	}
	out := filepath.Join(t.TempDir(), "m.json")
	if err := writeManifest(out, nil); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, out); strings.TrimSpace(got) != "[]" {
		t.Errorf("nil manifest written as %q, want []", got)
	}
	// writeManifest under a file path errors.
	blocker := filepath.Join(t.TempDir(), "blocker")
	writeFile(t, blocker, "x")
	if err := writeManifest(filepath.Join(blocker, "m.json"), nil); err == nil {
		t.Error("writeManifest under a file path should error")
	}
}

func TestPathHelpers(t *testing.T) {
	if !filepath.IsAbs(absJoin("/base", "rel")) {
		t.Error("absJoin should return absolute")
	}
	if absJoin("/base", "/abs/path") != "/abs/path" {
		t.Errorf("absJoin abs = %q", absJoin("/base", "/abs/path"))
	}
	if !filepath.IsAbs(mustAbs("rel")) {
		t.Error("mustAbs should return absolute")
	}
	if cwd() == "" {
		t.Error("cwd should be non-empty")
	}
}

func TestCopyInProfileErrors(t *testing.T) {
	var buf bytes.Buffer
	if err := CopyIn(&buf, filepath.Join(t.TempDir(), "missing.json"), t.TempDir(), filepath.Join(t.TempDir(), "m.json"), false); err == nil {
		t.Error("CopyIn with missing profile should error")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	writeFile(t, bad, "not json")
	if err := CopyIn(&buf, bad, t.TempDir(), filepath.Join(t.TempDir(), "m.json"), false); err == nil {
		t.Error("CopyIn with malformed profile should error")
	}
}

// TestCopyOutOverwriteAndMissing: push always overwrites the origin (even one
// changed out-of-band) and reports entries whose sandbox copy is missing.
func TestCopyOutOverwriteAndMissing(t *testing.T) {
	base := t.TempDir()
	origin := filepath.Join(base, "origin.txt")
	writeFile(t, origin, "external\n") // changed out-of-band; push overwrites anyway
	sandboxFile := filepath.Join(base, "sb.txt")
	writeFile(t, sandboxFile, "from-sandbox\n")
	gone := filepath.Join(base, "gone.txt")

	manifest := filepath.Join(base, "m.json")
	entries := []ManifestEntry{
		{Mode: "rw", Origin: origin, SandboxPath: sandboxFile},
		{Mode: "rw", Origin: filepath.Join(base, "x"), SandboxPath: gone}, // sandbox missing
		{Mode: "ro", Origin: origin, SandboxPath: sandboxFile},            // ro skipped
	}
	if err := writeManifest(manifest, entries); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := CopyOut(&buf, manifest); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "missing") {
		t.Errorf("CopyOut output = %q, want a missing note", buf.String())
	}
	if got := readFile(t, origin); got != "from-sandbox\n" {
		t.Errorf("origin after push = %q, want from-sandbox (overwritten)", got)
	}
}
