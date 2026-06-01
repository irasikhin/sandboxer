package srcs

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestMatchEntriesNameRegexDepth(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tree")
	writeFile(t, filepath.Join(root, "a.go"), "1")
	writeFile(t, filepath.Join(root, "sub", "b.go"), "2")
	writeFile(t, filepath.Join(root, "sub", "c.txt"), "3")

	// name matches by basename, across all depths.
	got, err := matchEntries(Src{Root: root, Name: "*.go"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("name *.go matched %d, want 2: %+v", len(got), got)
	}

	// regex matches against the slash rel path.
	got, err = matchEntries(Src{Root: root, Regex: `\.txt$`}, "")
	if err != nil || len(got) != 1 {
		t.Errorf("regex matched %d (err=%v), want 1", len(got), err)
	}

	// depth=1 stays at the top level only.
	got, _ = matchEntries(Src{Root: root, Name: "*.go", Depth: 1}, "")
	if len(got) != 1 {
		t.Errorf("depth=1 matched %d, want 1 (a.go only): %+v", len(got), got)
	}

	// bad regex propagates an error.
	if _, err := matchEntries(Src{Root: root, Regex: "["}, ""); err == nil {
		t.Error("bad regex should error")
	}
	// no matcher field → error.
	if _, err := matchEntries(Src{Root: root}, ""); err == nil {
		t.Error("matcher without name/glob/regex should error")
	}
}

func TestResolveTargets(t *testing.T) {
	dep := filepath.Join(t.TempDir(), "lib")
	writeFile(t, filepath.Join(dep, "f"), "x")
	sandbox := t.TempDir()

	// Explicit From with no To → To defaults to basename.
	ts, err := resolveTargets(Profile{Srcs: []Src{{From: dep, Mode: "rw"}}}, sandbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 1 || filepath.Base(ts[0].Dest) != "lib" || ts[0].Mode != "rw" {
		t.Errorf("explicit target = %+v", ts)
	}

	// An entry with nothing set is an error.
	if _, err := resolveTargets(Profile{Srcs: []Src{{}}}, sandbox); err == nil {
		t.Error("empty srcs entry should error")
	}
}

func TestSig(t *testing.T) {
	if sig(filepath.Join(t.TempDir(), "missing")) != "" {
		t.Error("missing path sig should be empty")
	}
	f := filepath.Join(t.TempDir(), "f")
	writeFile(t, f, "data")
	if s := sig(f); !strings.HasPrefix(s, "f:") {
		t.Errorf("file sig = %q, want f: prefix", s)
	}
	d := t.TempDir()
	writeFile(t, filepath.Join(d, "x"), "y")
	if s := sig(d); !strings.HasPrefix(s, "d:") {
		t.Errorf("dir sig = %q, want d: prefix", s)
	}
}

func TestCopyFileAndPathErrors(t *testing.T) {
	if err := copyFile(filepath.Join(t.TempDir(), "nope"), filepath.Join(t.TempDir(), "dst"), 0o644); err == nil {
		t.Error("copyFile missing src should error")
	}
	if err := copyPath(filepath.Join(t.TempDir(), "nope"), filepath.Join(t.TempDir(), "dst")); err == nil {
		t.Error("copyPath missing src should error")
	}
	// copyPath on a directory recreates the tree.
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a", "b.txt"), "hi")
	dst := filepath.Join(t.TempDir(), "out")
	if err := copyPath(src, dst); err != nil {
		t.Fatalf("copyPath dir: %v", err)
	}
	if !exists(filepath.Join(dst, "a", "b.txt")) {
		t.Error("copyPath did not recreate nested file")
	}
}

func TestCopyAndManifestWriteErrors(t *testing.T) {
	// A file standing where a parent directory is expected makes MkdirAll fail.
	blocker := filepath.Join(t.TempDir(), "blocker")
	writeFile(t, blocker, "x")
	src := filepath.Join(t.TempDir(), "src")
	writeFile(t, src, "data")
	if err := copyFile(src, filepath.Join(blocker, "child"), 0o644); err == nil {
		t.Error("copyFile under a file path should error")
	}
	// writeManifest to an unwritable path errors.
	if err := writeManifest(filepath.Join(blocker, "m.json"), nil); err == nil {
		t.Error("writeManifest under a file path should error")
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

func TestCopyOutMissingAndForce(t *testing.T) {
	base := t.TempDir()
	origin := filepath.Join(base, "origin.txt")
	writeFile(t, origin, "v0\n")
	sandboxFile := filepath.Join(base, "sb.txt")
	writeFile(t, sandboxFile, "from-sandbox\n")
	gone := filepath.Join(base, "gone.txt")

	manifest := filepath.Join(base, "m.json")
	entries := []ManifestEntry{
		{Mode: "rw", Origin: origin, SandboxPath: sandboxFile, OriginSig: "stale-sig"},
		{Mode: "rw", Origin: filepath.Join(base, "x"), SandboxPath: gone}, // sandbox missing → counted
		{Mode: "ro", Origin: origin, SandboxPath: sandboxFile},            // ro skipped
	}
	if err := writeManifest(manifest, entries); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	// Without force: origin's real sig != stale-sig → SKIP.
	if err := CopyOut(&buf, manifest, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "SKIP") || !strings.Contains(buf.String(), "missing") {
		t.Errorf("CopyOut output = %q, want SKIP + missing", buf.String())
	}

	// With force: origin overwritten by sandbox content.
	buf.Reset()
	if err := CopyOut(&buf, manifest, true); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, origin); got != "from-sandbox\n" {
		t.Errorf("origin after force = %q, want from-sandbox", got)
	}
}
