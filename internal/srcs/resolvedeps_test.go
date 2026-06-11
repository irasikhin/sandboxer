package srcs

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveDeps pins the four resolution outcomes: no deps, a path that would
// escape the sandbox (refused), a dep with no match, and a dep matched under
// more than one root (warns, uses the first).
func TestResolveDeps(t *testing.T) {
	sb := t.TempDir()
	w := &bytes.Buffer{}

	// no deps → nil
	if got := resolveDeps(Profile{}, sb, w, false); got != nil {
		t.Errorf("resolveDeps(no deps) = %v, want nil", got)
	}

	// a ../ dep escapes the sandbox → refused, nothing copied
	w.Reset()
	if out := resolveDeps(Profile{Roots: []string{t.TempDir()}, Deps: []string{"../escape"}}, sb, w, false); len(out) != 0 ||
		!strings.Contains(w.String(), "refusing to copy outside") {
		t.Errorf("escape dep: targets=%v log=%q", out, w.String())
	}

	// a dep with no match under the roots → skipped with a not-found note
	w.Reset()
	if out := resolveDeps(Profile{Roots: []string{t.TempDir()}, Deps: []string{"missing"}}, sb, w, false); len(out) != 0 ||
		!strings.Contains(w.String(), "not found") {
		t.Errorf("missing dep: targets=%v log=%q", out, w.String())
	}

	// a dep matched once → exactly one copy job
	w.Reset()
	r1 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(r1, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out := resolveDeps(Profile{Roots: []string{r1}, Deps: []string{"lib"}}, sb, w, false); len(out) != 1 {
		t.Fatalf("single match: want 1 target, got %d (log %q)", len(out), w.String())
	}

	// a dep matched under two roots → warns, still one job (the first)
	w.Reset()
	r2, r3 := t.TempDir(), t.TempDir()
	for _, r := range []string{r2, r3} {
		if err := os.MkdirAll(filepath.Join(r, "lib"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if out := resolveDeps(Profile{Roots: []string{r2, r3}, Deps: []string{"lib"}}, sb, w, false); len(out) != 1 ||
		!strings.Contains(w.String(), "matches under roots") {
		t.Errorf("multi-match: targets=%d log=%q", len(out), w.String())
	}
}

// TestResolveDepsUnmountedRootsHint: inside the container, a root that isn't
// visible on the filesystem (host roots aren't bind-mounted) yields a hint that
// points to a host-side pull instead of silently finding nothing. The same
// missing root prints no hint outside the container.
func TestResolveDepsUnmountedRootsHint(t *testing.T) {
	sb := t.TempDir()
	missing := filepath.Join(t.TempDir(), "not-mounted")
	w := &bytes.Buffer{}

	if out := resolveDeps(Profile{Roots: []string{missing}, Deps: []string{"lib"}}, sb, w, true); len(out) != 0 ||
		!strings.Contains(w.String(), "host roots aren't mounted") {
		t.Errorf("in-container missing root: targets=%v log=%q", out, w.String())
	}

	w.Reset()
	if resolveDeps(Profile{Roots: []string{missing}, Deps: []string{"lib"}}, sb, w, false); strings.Contains(w.String(), "host roots aren't mounted") {
		t.Errorf("host-side resolve should not print the in-container hint: %q", w.String())
	}

	// In-container but the root IS visible (e.g. a mounted origin path) → no hint.
	w.Reset()
	if resolveDeps(Profile{Roots: []string{t.TempDir()}, Deps: []string{"lib"}}, sb, w, true); strings.Contains(w.String(), "host roots aren't mounted") {
		t.Errorf("visible root should not trigger the unmounted-roots hint: %q", w.String())
	}
}
