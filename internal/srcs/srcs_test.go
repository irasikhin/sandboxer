package srcs

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGlobToRe(t *testing.T) {
	cases := []struct {
		glob  string
		input string
		want  bool
	}{
		// ** -> .* (matches across "/"); tested against rel paths.
		{"**/*.proto", "a/b/c.proto", true},
		{"**/*.proto", "x.proto", true},
		{"**/*.proto", "a/b/c.txt", false},
		// * -> [^/]* (does not cross "/").
		{"*.go", "main.go", true},
		{"*.go", "a/main.go", false},
		// ? -> [^/] (single non-slash char).
		{"?.txt", "a.txt", true},
		{"?.txt", "ab.txt", false},
	}
	for _, c := range cases {
		got := globToRe(c.glob).MatchString(c.input)
		if got != c.want {
			t.Errorf("globToRe(%q).Match(%q) = %v, want %v", c.glob, c.input, got, c.want)
		}
	}
}

// writeFile writes content to path, creating parents.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// writeProfile marshals p to a temp profile.json and returns its path.
func writeProfile(t *testing.T, dir string, p Profile) string {
	t.Helper()
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	pf := filepath.Join(dir, "profile.json")
	writeFile(t, pf, string(data))
	return pf
}

func TestResolveExplicitRoundTrip(t *testing.T) {
	base := t.TempDir()
	originDir := filepath.Join(base, "origin", "pkg")
	writeFile(t, filepath.Join(originDir, "file.txt"), "original\n")

	sandbox := filepath.Join(base, "sandbox")
	manifest := filepath.Join(base, "manifest.json")

	profile := Profile{Srcs: []Src{{From: originDir, To: "pkg", Mode: "rw"}}}
	pf := writeProfile(t, base, profile)

	var out bytes.Buffer
	if err := CopyIn(&out, pf, sandbox, manifest, false); err != nil {
		t.Fatalf("CopyIn: %v", err)
	}

	dest := filepath.Join(sandbox, "pkg", "file.txt")
	if got := readFile(t, dest); got != "original\n" {
		t.Fatalf("dest after pull = %q, want original", got)
	}

	// Modify the copy inside the sandbox, then push back.
	writeFile(t, dest, "modified\n")
	out.Reset()
	if err := CopyOut(&out, manifest); err != nil {
		t.Fatalf("CopyOut: %v", err)
	}

	originFile := filepath.Join(originDir, "file.txt")
	if got := readFile(t, originFile); got != "modified\n" {
		t.Fatalf("origin after push = %q, want modified", got)
	}
}

func TestMatcherGlob(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "tree")
	writeFile(t, filepath.Join(root, "a", "one.proto"), "1")
	writeFile(t, filepath.Join(root, "b", "c", "two.proto"), "2")
	writeFile(t, filepath.Join(root, "b", "note.txt"), "x")
	writeFile(t, filepath.Join(root, ".git", "hidden.proto"), "skip me")

	sandbox := filepath.Join(base, "sandbox")
	manifest := filepath.Join(base, "manifest.json")

	profile := Profile{
		MainSrc: root,
		Srcs:    []Src{{Glob: "**/*.proto", To: "vendor", Mode: "rw"}},
	}
	pf := writeProfile(t, base, profile)

	var out bytes.Buffer
	if err := CopyIn(&out, pf, sandbox, manifest, false); err != nil {
		t.Fatalf("CopyIn: %v", err)
	}

	if !exists(filepath.Join(sandbox, "vendor", "a", "one.proto")) {
		t.Error("expected a/one.proto copied")
	}
	if !exists(filepath.Join(sandbox, "vendor", "b", "c", "two.proto")) {
		t.Error("expected b/c/two.proto copied")
	}
	if exists(filepath.Join(sandbox, "vendor", "b", "note.txt")) {
		t.Error("note.txt should not match *.proto")
	}
	if exists(filepath.Join(sandbox, "vendor", ".git", "hidden.proto")) {
		t.Error(".git proto should be skipped")
	}

	m := readManifest(manifest)
	if len(m) != 2 {
		t.Fatalf("manifest entries = %d, want 2", len(m))
	}
}

func TestPullKeepPushOverwrite(t *testing.T) {
	base := t.TempDir()
	originDir := filepath.Join(base, "origin", "pkg")
	originFile := filepath.Join(originDir, "file.txt")
	writeFile(t, originFile, "v1\n")

	sandbox := filepath.Join(base, "sandbox")
	manifest := filepath.Join(base, "manifest.json")
	dest := filepath.Join(sandbox, "pkg", "file.txt")

	profile := Profile{Srcs: []Src{{From: originDir, To: "pkg", Mode: "rw"}}}
	pf := writeProfile(t, base, profile)

	var out bytes.Buffer
	if err := CopyIn(&out, pf, sandbox, manifest, false); err != nil {
		t.Fatalf("CopyIn 1: %v", err)
	}

	// --- KEEP: an existing target is kept on re-pull without force. ---
	writeFile(t, dest, "local-change\n")
	out.Reset()
	if err := CopyIn(&out, pf, sandbox, manifest, false); err != nil {
		t.Fatalf("CopyIn 2: %v", err)
	}
	if !strings.Contains(out.String(), "KEEP") {
		t.Errorf("expected KEEP message, got: %q", out.String())
	}
	if got := readFile(t, dest); got != "local-change\n" {
		t.Errorf("dest after KEEP = %q, want local-change", got)
	}

	// --- force pull: overwritten from origin. ---
	out.Reset()
	if err := CopyIn(&out, pf, sandbox, manifest, true); err != nil {
		t.Fatalf("CopyIn force: %v", err)
	}
	if got := readFile(t, dest); got != "v1\n" {
		t.Errorf("dest after force pull = %q, want v1", got)
	}

	// --- push always overwrites the origin (depsync semantics), even one
	//     changed out-of-band. ---
	writeFile(t, dest, "sandbox-edit\n")
	writeFile(t, originFile, "external-edit\n")
	out.Reset()
	if err := CopyOut(&out, manifest); err != nil {
		t.Fatalf("CopyOut: %v", err)
	}
	if !strings.Contains(out.String(), "PUSH") {
		t.Errorf("expected PUSH message, got: %q", out.String())
	}
	if got := readFile(t, originFile); got != "sandbox-edit\n" {
		t.Errorf("origin after push = %q, want sandbox-edit (overwritten)", got)
	}
}
