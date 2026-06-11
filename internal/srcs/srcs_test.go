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
	if err := CopyIn(&out, PullOpts{ProfileFile: pf, SandboxDir: sandbox, ManifestFile: manifest}); err != nil {
		t.Fatalf("CopyIn: %v", err)
	}
	dest := filepath.Join(sandbox, "pkg", "file.txt")
	if got := readFile(t, dest); got != "original\n" {
		t.Fatalf("dest after pull = %q, want original", got)
	}

	// Modify the copy, then push back over the (untouched) origin: the recorded
	// signature still matches, so the default push goes through.
	writeFile(t, dest, "modified\n")
	out.Reset()
	if err := CopyOut(&out, manifest, PushOpts{}); err != nil {
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
	if err := CopyIn(&out, PullOpts{ProfileFile: pf, SandboxDir: sandbox, ManifestFile: manifest}); err != nil {
		t.Fatalf("CopyIn 1: %v", err)
	}

	// --- KEEP: an existing target is kept on re-pull without force. ---
	writeFile(t, dest, "local-change\n")
	out.Reset()
	if err := CopyIn(&out, PullOpts{ProfileFile: pf, SandboxDir: sandbox, ManifestFile: manifest}); err != nil {
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
	if err := CopyIn(&out, PullOpts{ProfileFile: pf, SandboxDir: sandbox, ManifestFile: manifest, Force: true}); err != nil {
		t.Fatalf("CopyIn force: %v", err)
	}
	if got := readFile(t, dest); got != "v1\n" {
		t.Errorf("dest after force pull = %q, want v1", got)
	}

	// --- an origin changed out-of-band: the default push SKIPs it (the host
	//     edit survives); --force restores the wholesale overwrite. ---
	writeFile(t, dest, "sandbox-edit\n")
	writeFile(t, originFile, "external-edit\n")
	out.Reset()
	if err := CopyOut(&out, manifest, PushOpts{}); err != nil {
		t.Fatalf("CopyOut: %v", err)
	}
	if !strings.Contains(out.String(), "changed on the host") {
		t.Errorf("expected a changed-on-host SKIP, got: %q", out.String())
	}
	if got := readFile(t, originFile); got != "external-edit\n" {
		t.Errorf("origin after default push = %q, want external-edit (preserved)", got)
	}
	out.Reset()
	if err := CopyOut(&out, manifest, PushOpts{Force: true}); err != nil {
		t.Fatalf("CopyOut force: %v", err)
	}
	if !strings.Contains(out.String(), "PUSH") {
		t.Errorf("expected PUSH message, got: %q", out.String())
	}
	if got := readFile(t, originFile); got != "sandbox-edit\n" {
		t.Errorf("origin after push --force = %q, want sandbox-edit (overwritten)", got)
	}
}

// TestPushSigRefresh: a successful push records the freshly written origin as
// the new sync point, so the next default push is not spuriously skipped; a
// KEEP on re-pull carries the OLD signature forward, so a host edit made
// before that pull still blocks the push.
func TestPushSigRefresh(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	originFile := filepath.Join(root, "pkg", "file.txt")
	writeFile(t, originFile, "v1\n")
	sandbox := filepath.Join(base, "sandbox")
	manifest := filepath.Join(base, "manifest.json")
	dest := filepath.Join(sandbox, "pkg", "file.txt")
	pf := writeProfile(t, base, Profile{Roots: []string{root}, Deps: []string{"pkg"}})
	pull := PullOpts{ProfileFile: pf, SandboxDir: sandbox, ManifestFile: manifest}

	var out bytes.Buffer
	if err := CopyIn(&out, pull); err != nil {
		t.Fatal(err)
	}
	// push #1 rewrites the origin and refreshes its signature in the manifest…
	writeFile(t, dest, "edit-1\n")
	if err := CopyOut(&out, manifest, PushOpts{}); err != nil {
		t.Fatal(err)
	}
	// …so push #2 (origin untouched since push #1) must go through, not skip.
	writeFile(t, dest, "edit-2\n")
	out.Reset()
	if err := CopyOut(&out, manifest, PushOpts{}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, originFile); got != "edit-2\n" {
		t.Errorf("origin after second default push = %q, want edit-2 (sig not refreshed?)", got)
	}

	// Host edit, then a KEEP re-pull: the old signature must be carried forward
	// (NOT re-blessed from the edited origin), so the default push still skips.
	writeFile(t, originFile, "host-edit\n")
	out.Reset()
	if err := CopyIn(&out, pull); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "KEEP") {
		t.Fatalf("expected KEEP on re-pull, got: %q", out.String())
	}
	out.Reset()
	if err := CopyOut(&out, manifest, PushOpts{}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, originFile); got != "host-edit\n" {
		t.Errorf("origin after KEEP+push = %q, want host-edit (old sig must block)", got)
	}
}

// TestPullSelfOriginKept: when the matched origin IS the sandbox copy (the
// in-container case — the sandbox dir is the cwd and thus a search root), the
// target is kept even under --force: copying a path onto itself would destroy
// it (copyEntry removes the destination first).
func TestPullSelfOriginKept(t *testing.T) {
	sandbox := t.TempDir()
	writeFile(t, filepath.Join(sandbox, "pkg", "file.txt"), "vendored\n")
	manifest := filepath.Join(t.TempDir(), "m.json")
	pf := writeProfile(t, t.TempDir(), Profile{Roots: []string{sandbox}, Deps: []string{"pkg"}})

	var out bytes.Buffer
	if err := CopyIn(&out, PullOpts{ProfileFile: pf, SandboxDir: sandbox, ManifestFile: manifest, Force: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "origin is the sandbox copy") {
		t.Errorf("expected the self-origin KEEP, got: %q", out.String())
	}
	if got := readFile(t, filepath.Join(sandbox, "pkg", "file.txt")); got != "vendored\n" {
		t.Errorf("self-origin pull destroyed the copy: %q", got)
	}
}
