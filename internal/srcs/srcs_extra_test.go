package srcs

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMatchEntriesNameRegexDepth(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tree")
	writeFile(t, filepath.Join(root, "a.go"), "1")
	writeFile(t, filepath.Join(root, "sub", "b.go"), "2")
	writeFile(t, filepath.Join(root, "sub", "c.txt"), "3")

	got, err := matchEntries(Src{Root: root, Name: "*.go"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("name *.go matched %d, want 2: %+v", len(got), got)
	}

	got, err = matchEntries(Src{Root: root, Regex: `\.txt$`}, "")
	if err != nil || len(got) != 1 {
		t.Errorf("regex matched %d (err=%v), want 1", len(got), err)
	}

	got, _ = matchEntries(Src{Root: root, Name: "*.go", Depth: 1}, "")
	if len(got) != 1 {
		t.Errorf("depth=1 matched %d, want 1 (a.go only): %+v", len(got), got)
	}

	if _, err := matchEntries(Src{Root: root, Regex: "["}, ""); err == nil {
		t.Error("bad regex should error")
	}
	if _, err := matchEntries(Src{Root: root}, ""); err == nil {
		t.Error("matcher without name/glob/regex should error")
	}
}

func TestResolveTargets(t *testing.T) {
	dep := filepath.Join(t.TempDir(), "lib")
	writeFile(t, filepath.Join(dep, "f"), "x")
	sandbox := t.TempDir()

	ts, err := resolveTargets(Profile{Srcs: []Src{{From: dep, Mode: "rw"}}}, sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 1 || filepath.Base(ts[0].Dest) != "lib" || ts[0].Mode != "rw" {
		t.Errorf("explicit target = %+v", ts)
	}
	if _, err := resolveTargets(Profile{Srcs: []Src{{}}}, sandbox); err == nil {
		t.Error("empty srcs entry should error")
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
	if orDefault("", "d") != "d" || orDefault("x", "d") != "x" {
		t.Error("orDefault")
	}
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
