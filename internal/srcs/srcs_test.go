package srcs

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestRoundTrip(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	writeFile(t, filepath.Join(root, "pkg", "file.txt"), "original\n")

	sandbox := filepath.Join(base, "sandbox")
	manifest := filepath.Join(base, "manifest.json")

	profile := Profile{Roots: []string{root}, Deps: []string{"pkg"}}
	pf := writeProfile(t, base, profile)

	var out bytes.Buffer
	if err := CopyIn(&out, pf, sandbox, manifest, false); err != nil {
		t.Fatalf("CopyIn: %v", err)
	}
	dest := filepath.Join(sandbox, "pkg", "file.txt")
	if got := readFile(t, dest); got != "original\n" {
		t.Fatalf("dest after pull = %q, want original", got)
	}

	// Modify the copy, then push back over the origin.
	writeFile(t, dest, "modified\n")
	out.Reset()
	if err := CopyOut(&out, manifest); err != nil {
		t.Fatalf("CopyOut: %v", err)
	}
	if got := readFile(t, filepath.Join(root, "pkg", "file.txt")); got != "modified\n" {
		t.Fatalf("origin after push = %q, want modified", got)
	}
}

func TestPullKeepPushOverwrite(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	originFile := filepath.Join(root, "pkg", "file.txt")
	writeFile(t, originFile, "v1\n")

	sandbox := filepath.Join(base, "sandbox")
	manifest := filepath.Join(base, "manifest.json")
	dest := filepath.Join(sandbox, "pkg", "file.txt")

	profile := Profile{Roots: []string{root}, Deps: []string{"pkg"}}
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

	// --- push always overwrites the origin (depsync), even one changed
	//     out-of-band. ---
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
