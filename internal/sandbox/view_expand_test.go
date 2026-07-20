package sandbox

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestIncludeIsPattern(t *testing.T) {
	for _, tc := range []struct {
		entry string
		want  bool
	}{
		{"/src/proto/", false},
		{"/a/b/c", false},
		{"/my-svc/v1.2/", false},
		{"/**/x/", true},
		{"**/x/", true},
		{"/services/*/", true},
		{"/src?/", true},
		{"/src[ab]/", true},
		{"/a/**", true},
	} {
		if got := includeIsPattern(tc.entry); got != tc.want {
			t.Errorf("includeIsPattern(%q) = %v, want %v", tc.entry, got, tc.want)
		}
	}
}

// expandTree builds the fixture worktree every expansion test walks:
//
//	x/                    top-level match
//	a/b/x/                deep match
//	a/x/                  mid match
//	a/x/x/                nested INSIDE a match — must be pruned
//	.git/x/               inside .git — never matched
//	services/{alpha,beta} for single-star tests
//	services/f.txt        a FILE star must never select
//	esc -> <outside>      symlink to a dir that contains x/ — never followed
func expandTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"x", "a/b/x", "a/x/x", ".git/x", "services/alpha", "services/beta"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "services", "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "esc")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	return root
}

func TestExpandInclude(t *testing.T) {
	root := expandTree(t)
	abs := func(rels ...string) []string {
		out := make([]string, len(rels))
		for i, r := range rels {
			out[i] = filepath.Join(root, filepath.FromSlash(r))
		}
		return out
	}
	for _, tc := range []struct {
		name    string
		pattern string
		want    []string // nil = no match; order is the walker's (DFS, ReadDir-sorted)
	}{
		// a/x is matched and PRUNED, so a/x/x never appears; .git/x and the
		// x/ behind the esc symlink are never considered.
		{name: "any depth", pattern: "/**/x/", want: abs("a/b/x", "a/x", "x")},
		{name: "unanchored sugar", pattern: "**/x/", want: abs("a/b/x", "a/x", "x")},
		{name: "redundant double recursion", pattern: "/**/**/x/", want: abs("a/b/x", "a/x", "x")},
		{name: "star selects direct child dirs only", pattern: "/services/*/", want: abs("services/alpha", "services/beta")},
		{name: "trailing double-star is the parent alone", pattern: "/a/**", want: abs("a")},
		{name: "anchored below root", pattern: "/a/**/x/", want: abs("a/b/x", "a/x")},
		{name: "a file is never selected", pattern: "/services/f*", want: nil},
		{name: "no match", pattern: "/**/nope/", want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := expandInclude(root, tc.pattern)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("expandInclude(%q) = %v, want %v", tc.pattern, got, tc.want)
			}
			// deterministic: a second walk yields the identical list
			again, err := expandInclude(root, tc.pattern)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(again, got) {
				t.Errorf("expandInclude(%q) is not deterministic: %v vs %v", tc.pattern, got, again)
			}
		})
	}
}

// TestViewDirsPatternZeroMatch: a pattern selecting nothing fails the resolve
// (fail closed — the sandbox would otherwise come up silently empty), naming
// the pattern and the branch.
func TestViewDirsPatternZeroMatch(t *testing.T) {
	root := t.TempDir()
	s := Source{RepoRoot: root, Path: root, Branch: "feat/x", Include: []string{"/**/nope/"}}
	_, err := ViewDirs(s)
	if err == nil {
		t.Fatal("ViewDirs accepted a pattern matching nothing")
	}
	for _, want := range []string{"matches no directory", "/**/nope/", "feat/x"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
	if err := checkViewDirs(s); err == nil || !strings.Contains(err.Error(), "matches no directory") {
		t.Errorf("checkViewDirs = %v, want the same zero-match refusal", err)
	}
}

// TestExpandIncludeUnreadableDirFailsClosed: a directory the walker cannot
// read fails the whole mount resolve. Silently skipping it would narrow the
// view without saying so — the sandbox would come up missing sources.
func TestExpandIncludeUnreadableDirFailsClosed(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root := t.TempDir()
	sealed := filepath.Join(root, "sealed")
	if err := os.MkdirAll(filepath.Join(sealed, "proto"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sealed, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sealed, 0o755) })

	srcs := []Source{{RepoRoot: root, Path: root, Branch: "feat/x", Managed: true, Include: []string{"/**/proto/"}}}
	_, _, err := Mounts(srcs)
	if err == nil {
		t.Fatal("Mounts resolved a pattern through an unreadable dir, want a refusal")
	}
	if !strings.Contains(err.Error(), "/**/proto/") {
		t.Errorf("error = %q, want it to name the pattern", err)
	}
}

// TestMountsPatternDynamicSet pins the property that makes patterns safe to
// resolve live: the mount set is recomputed against the host on every call, so
// a NEW directory matching the pattern grows the set — a different argv, a
// different fingerprint, a session rebuild on the next enter. The container
// cannot trigger this itself: an unmatched location is not mounted, so nothing
// the container writes can widen its own wall.
func TestMountsPatternDynamicSet(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "svc1", "proto"), 0o755); err != nil {
		t.Fatal(err)
	}
	srcs := []Source{{RepoRoot: root, Path: root, Managed: true, Include: []string{"/**/proto/"}}}

	mountDest, before, err := Mounts(srcs)
	if err != nil {
		t.Fatal(err)
	}
	if mountDest || len(before) != 1 {
		t.Fatalf("Mounts = (%v, %v), want the single matched dir and no root mount", mountDest, before)
	}
	fpBefore := MountFingerprint(before)

	if err := os.MkdirAll(filepath.Join(root, "svc2", "proto"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, after, err := Mounts(srcs)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 2 {
		t.Fatalf("Mounts after mkdir = %v, want both proto dirs", after)
	}
	if fpAfter := MountFingerprint(after); fpAfter == fpBefore {
		t.Error("fingerprint did not change with the mount set — the session would keep the stale view")
	}
}
