package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mkdirs creates repo-relative directories under root (slashed, like include
// entries) for the tests that need a worktree on disk.
func mkdirs(t *testing.T, root string, rel ...string) {
	t.Helper()
	for _, r := range rel {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(r)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// TestMountsNegationMasks pins the exclusion composition: the exposed dir stays
// a mount, the negated subtree becomes a mask target, and the sandbox stays
// narrowed (the negation changes content, not the mount shape).
func TestMountsNegationMasks(t *testing.T) {
	wt := t.TempDir()
	mkdirs(t, wt, "src/vendor", "src/a/b", "keep")
	abs := func(parts ...string) string {
		return filepath.Join(append([]string{wt}, parts...)...)
	}

	t.Run("literal negation carves the exposure", func(t *testing.T) {
		s := Source{RepoRoot: wt, Path: wt, Managed: true, Branch: "feat/x",
			Include: []string{"/src/", "!/src/vendor/"}}
		mountDest, mounts, masks, err := Mounts([]Source{s})
		if err != nil {
			t.Fatal(err)
		}
		if mountDest {
			t.Error("mountDest = true — a negation must not mount the whole root")
		}
		if len(mounts) != 1 || mounts[0] != abs("src") {
			t.Errorf("mounts = %v, want [%s]", mounts, abs("src"))
		}
		if len(masks) != 1 || masks[0] != abs("src", "vendor") {
			t.Errorf("masks = %v, want [%s]", masks, abs("src", "vendor"))
		}
	})

	t.Run("deep negation", func(t *testing.T) {
		s := Source{RepoRoot: wt, Path: wt, Managed: true, Branch: "feat/x",
			Include: []string{"/src/", "!/src/a/b/"}}
		_, mounts, masks, err := Mounts([]Source{s})
		if err != nil {
			t.Fatal(err)
		}
		if len(mounts) != 1 || mounts[0] != abs("src") {
			t.Errorf("mounts = %v, want [%s]", mounts, abs("src"))
		}
		if len(masks) != 1 || masks[0] != abs("src", "a", "b") {
			t.Errorf("masks = %v, want [%s]", masks, abs("src", "a", "b"))
		}
	})

	t.Run("a negation outside every exposed dir is ignored", func(t *testing.T) {
		s := Source{RepoRoot: wt, Path: wt, Managed: true, Branch: "feat/x",
			Include: []string{"/src/", "!/keep/"}}
		mountDest, mounts, masks, err := Mounts([]Source{s})
		if err != nil {
			t.Fatalf("a negation under no exposed dir must not error: %v", err)
		}
		if mountDest {
			t.Error("mountDest = true")
		}
		if len(mounts) != 1 || mounts[0] != abs("src") {
			t.Errorf("mounts = %v, want the exposed dir untouched", mounts)
		}
		if len(masks) != 0 {
			t.Errorf("masks = %v, want none (already invisible)", masks)
		}
	})

	t.Run("a positive at the negation is dropped", func(t *testing.T) {
		s := Source{RepoRoot: wt, Path: wt, Managed: true, Branch: "feat/x",
			Include: []string{"/src/", "/src/vendor/", "!/src/vendor/"}}
		_, mounts, masks, err := Mounts([]Source{s})
		if err != nil {
			t.Fatal(err)
		}
		if len(mounts) != 1 || mounts[0] != abs("src") {
			t.Errorf("mounts = %v, want only %s (the excluded child dropped)", mounts, abs("src"))
		}
		if len(masks) != 1 || masks[0] != abs("src", "vendor") {
			t.Errorf("masks = %v, want the exclusion to still mask %s", masks, abs("src", "vendor"))
		}
	})

	t.Run("all positives excluded is a hard error", func(t *testing.T) {
		s := Source{RepoRoot: wt, Path: wt, Managed: true, Branch: "feat/x",
			Include: []string{"/src/vendor/", "!/src/"}}
		_, _, _, err := Mounts([]Source{s})
		if err == nil {
			t.Fatal("Mounts accepted an include that exposes nothing")
		}
		for _, want := range []string{"/src/vendor/", "!/src/"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want it to name %q", err, want)
			}
		}
	})

	t.Run("nested negations keep only the shallow mask", func(t *testing.T) {
		s := Source{RepoRoot: wt, Path: wt, Managed: true, Branch: "feat/x",
			Include: []string{"/src/", "!/src/a/", "!/src/a/b/"}}
		_, mounts, masks, err := Mounts([]Source{s})
		if err != nil {
			t.Fatal(err)
		}
		if len(mounts) != 1 || mounts[0] != abs("src") {
			t.Errorf("mounts = %v, want [%s]", mounts, abs("src"))
		}
		if len(masks) != 1 || masks[0] != abs("src", "a") {
			t.Errorf("masks = %v, want only the shallow %s", masks, abs("src", "a"))
		}
	})
}

// TestMountsNegationPattern: an exclusion may be a pattern, expanded with the
// same on-disk rules as an exposure — zero matches is a hard error, never a
// silently narrower set.
func TestMountsNegationPattern(t *testing.T) {
	wt := t.TempDir()
	mkdirs(t, wt, "src/vendor", "src/deep/vendor", "src/keep")
	s := Source{RepoRoot: wt, Path: wt, Managed: true, Branch: "feat/x",
		Include: []string{"/src/", "!**/vendor/"}}
	_, mounts, masks, err := Mounts([]Source{s})
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 1 || mounts[0] != filepath.Join(wt, "src") {
		t.Errorf("mounts = %v, want the exposed /src", mounts)
	}
	want := []string{filepath.Join(wt, "src", "deep", "vendor"), filepath.Join(wt, "src", "vendor")}
	if len(masks) != 2 || masks[0] != want[0] || masks[1] != want[1] {
		t.Errorf("masks = %v, want %v", masks, want)
	}

	none := Source{RepoRoot: wt, Path: wt, Managed: true, Branch: "feat/x",
		Include: []string{"/src/", "!/**/nope/"}}
	_, _, _, err = Mounts([]Source{none})
	if err == nil {
		t.Fatal("a negation pattern matching nothing must be a hard error")
	}
	for _, wantMsg := range []string{"!/**/nope/", "matches no directory", "feat/x"} {
		if !strings.Contains(err.Error(), wantMsg) {
			t.Errorf("error = %q, want it to mention %q", err, wantMsg)
		}
	}
}

// TestMountsWholeRepoNegation: a negation does not narrow — ["**", "!/vendor/"]
// is whole-repo exposure with a mask, so the root mount stays and the mask is
// emitted on top of it. An adopted source in whole mode gets its own mount plus
// its mask.
func TestMountsWholeRepoNegation(t *testing.T) {
	wt := t.TempDir()
	mkdirs(t, wt, "vendor")
	managed := Source{RepoRoot: wt, Path: wt, Managed: true, Branch: "feat/x",
		Include: []string{"**", "!/vendor/"}}
	mountDest, mounts, masks, err := Mounts([]Source{managed})
	if err != nil {
		t.Fatal(err)
	}
	if !mountDest {
		t.Error("mountDest = false — a negation must not narrow the exposure")
	}
	if len(mounts) != 0 {
		t.Errorf("mounts = %v, want none (managed rides the root mount)", mounts)
	}
	if len(masks) != 1 || masks[0] != filepath.Join(wt, "vendor") {
		t.Errorf("masks = %v, want [%s]", masks, filepath.Join(wt, "vendor"))
	}

	adoptedPath := t.TempDir()
	adopted := Source{RepoRoot: "/repo", Path: adoptedPath, Managed: false,
		Include: []string{"**", "!/vendor/"}}
	mountDest, mounts, masks, err = Mounts([]Source{adopted})
	if err != nil {
		t.Fatal(err)
	}
	if !mountDest {
		t.Error("mountDest = false for an adopted whole-repo source")
	}
	if len(mounts) != 1 || mounts[0] != adoptedPath {
		t.Errorf("mounts = %v, want the adopted tree %q", mounts, adoptedPath)
	}
	if len(masks) != 1 || masks[0] != filepath.Join(adoptedPath, "vendor") {
		t.Errorf("masks = %v, want the mask inside the adopted mount", masks)
	}
}

// TestMountsMasksSortedUnderMounts: the mask list is sorted and every entry is a
// STRICT descendant of a mount in the same plan — never equal to one, never an
// ancestor of one. That invariant is what lets the argv emit all masks after
// all mounts.
func TestMountsMasksSortedUnderMounts(t *testing.T) {
	wt := t.TempDir()
	mkdirs(t, wt, "src/a/b", "src/vendor", "src/z")
	s := Source{RepoRoot: wt, Path: wt, Managed: true, Branch: "feat/x",
		Include: []string{"/src/", "/src/a/", "!/src/vendor/", "!/src/a/b/", "!/src/z/"}}
	_, mounts, masks, err := Mounts([]Source{s})
	if err != nil {
		t.Fatal(err)
	}
	if len(mounts) != 2 {
		t.Fatalf("mounts = %v, want both exposed dirs", mounts)
	}
	if len(masks) != 3 {
		t.Fatalf("masks = %v, want three masks", masks)
	}
	if !sortedStrings(masks) {
		t.Errorf("masks = %v, want sorted", masks)
	}
	sep := string(filepath.Separator)
	for _, m := range masks {
		under := false
		for _, p := range mounts {
			if m == p {
				t.Errorf("mask %q equals a mount", m)
			}
			if strings.HasPrefix(p, m+sep) {
				t.Errorf("mask %q is an ancestor of mount %q", m, p)
			}
			if strings.HasPrefix(m, p+sep) {
				under = true
			}
		}
		if !under {
			t.Errorf("mask %q is not strictly under any mount in the plan %v", m, mounts)
		}
	}
}

// TestCheckViewDirsNegations: an exclusion is validated like an exposure —
// existence on the branch, lexical and real-path containment — including when
// it sits next to a whole-repo "**".
func TestCheckViewDirsNegations(t *testing.T) {
	t.Run("a missing negation is refused", func(t *testing.T) {
		wt := t.TempDir()
		mkdirs(t, wt, "src")
		s := Source{RepoRoot: wt, Path: wt, Branch: "feat/x",
			Include: []string{"/src/", "!/src/nope/"}}
		err := checkViewDirs(s)
		if err == nil || !strings.Contains(err.Error(), "!/src/nope/") || !strings.Contains(err.Error(), "not a directory") {
			t.Errorf("checkViewDirs = %v, want a not-a-directory refusal naming the negation", err)
		}
	})

	t.Run("a negation symlink escaping is refused", func(t *testing.T) {
		root := t.TempDir()
		wt := filepath.Join(root, "wt")
		mkdirs(t, wt, "src")
		outside := filepath.Join(root, "outside")
		mkdirs(t, root, "outside")
		if err := os.Symlink(outside, filepath.Join(wt, "escape")); err != nil {
			t.Skipf("symlinks unsupported: %v", err)
		}
		s := Source{RepoRoot: wt, Path: wt, Branch: "feat/x",
			Include: []string{"/src/", "!/escape/"}}
		err := checkViewDirs(s)
		if err == nil || !strings.Contains(err.Error(), "outside the worktree") {
			t.Errorf("checkViewDirs = %v, want the escaping negation refused", err)
		}
	})

	t.Run("a valid negation passes", func(t *testing.T) {
		wt := t.TempDir()
		mkdirs(t, wt, "src/vendor")
		s := Source{RepoRoot: wt, Path: wt, Branch: "feat/x",
			Include: []string{"/src/", "!/src/vendor/"}}
		if err := checkViewDirs(s); err != nil {
			t.Errorf("checkViewDirs = %v, want nil", err)
		}
	})

	t.Run("a whole-repo include still validates its negations", func(t *testing.T) {
		wt := t.TempDir()
		s := Source{RepoRoot: wt, Path: wt, Branch: "feat/x",
			Include: []string{"**", "!/nope/"}}
		err := checkViewDirs(s)
		if err == nil || !strings.Contains(err.Error(), "!/nope/") {
			t.Errorf("checkViewDirs = %v, want the missing negation named", err)
		}
		mkdirs(t, wt, "nope")
		if err := checkViewDirs(s); err != nil {
			t.Errorf("checkViewDirs = %v, want nil once the negation exists", err)
		}
	})

	t.Run("positive messages are unchanged", func(t *testing.T) {
		wt := t.TempDir()
		mkdirs(t, wt, "vendor")
		s := Source{RepoRoot: wt, Path: wt, Branch: "feat/x",
			Include: []string{"/missing/", "!/vendor/"}}
		err := checkViewDirs(s)
		if err == nil || !strings.Contains(err.Error(), `include "/missing/" is not a directory on branch feat/x`) {
			t.Errorf("checkViewDirs = %v, want the original positive message", err)
		}
	})
}
