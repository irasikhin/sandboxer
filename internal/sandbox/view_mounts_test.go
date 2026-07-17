package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestViewDirs maps include directories onto absolute host paths under the
// worktree, and yields the worktree itself when nothing narrows the source.
func TestViewDirs(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  Source
		want []string
	}{
		{
			name: "no include is the worktree itself",
			src:  Source{Path: "/wt"},
			want: []string{"/wt"},
		},
		{
			name: "explicit catch-all is the worktree itself",
			src:  Source{Path: "/wt", Include: []string{"**"}},
			want: []string{"/wt"},
		},
		{
			name: "one directory",
			src:  Source{Path: "/wt", Include: []string{"/src/proto/"}},
			want: []string{"/wt/src/proto"},
		},
		{
			name: "several directories keep config order",
			src:  Source{Path: "/wt", Include: []string{"/b/", "/a/"}},
			want: []string{"/wt/b", "/wt/a"},
		},
		{
			name: "deep directory",
			src:  Source{Path: "/wt", Include: []string{"/a/b/c/"}},
			want: []string{"/wt/a/b/c"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ViewDirs(tc.src)
			if len(got) != len(tc.want) {
				t.Fatalf("ViewDirs = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != filepath.FromSlash(tc.want[i]) {
					t.Errorf("ViewDirs[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestMountsShape covers the switch between the sandbox's two mount shapes, and
// pins the invariant the whole containment model rests on: the moment ANY
// source is narrowed, the <slug>/ root must not be mounted — the worktrees
// under it are complete, so a root mount would expose every excluded file.
func TestMountsShape(t *testing.T) {
	managed := Source{Path: "/slug/repo/feat/x", Managed: true}
	adopted := Source{Path: "/elsewhere/other", Managed: false}
	narrowed := Source{Path: "/slug/repo/feat/x", Managed: true, Include: []string{"/src/proto/"}}

	t.Run("unnarrowed mounts the root, plus adopted trees only", func(t *testing.T) {
		mountDest, m := Mounts([]Source{managed, adopted})
		if !mountDest {
			t.Error("mountDest = false, want the root mounted")
		}
		if len(m) != 1 || m[0] != adopted.Path {
			t.Errorf("mounts = %v, want [%q] (managed rides the root mount)", m, adopted.Path)
		}
	})

	t.Run("narrowed never mounts the root", func(t *testing.T) {
		mountDest, m := Mounts([]Source{narrowed})
		if mountDest {
			t.Fatal("mountDest = true for a narrowed sandbox — the whole repo would be exposed")
		}
		want := filepath.FromSlash("/slug/repo/feat/x/src/proto")
		if len(m) != 1 || m[0] != want {
			t.Errorf("mounts = %v, want [%q]", m, want)
		}
	})

	t.Run("one narrowed source moves every source onto its own mount", func(t *testing.T) {
		// The root mount is all-or-nothing, so an unnarrowed source sharing the
		// sandbox must get an explicit mount or it would vanish.
		mountDest, m := Mounts([]Source{narrowed, {Path: "/slug/other/feat/x", Managed: true}, adopted})
		if mountDest {
			t.Fatal("mountDest = true — narrowing one source must not mount the root")
		}
		for _, want := range []string{"/slug/repo/feat/x/src/proto", "/slug/other/feat/x", "/elsewhere/other"} {
			if !containsPath(m, filepath.FromSlash(want)) {
				t.Errorf("mounts %v missing %q", m, want)
			}
		}
	})

	t.Run("mounts are sorted for a stable argv", func(t *testing.T) {
		_, m := Mounts([]Source{{Path: "/wt", Managed: true, Include: []string{"/z/", "/a/", "/m/"}}})
		if !sortedStrings(m) {
			t.Errorf("mounts = %v, want sorted (the session ConfigHash depends on the order)", m)
		}
	})
}

func containsPath(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func sortedStrings(s []string) bool {
	for i := 1; i < len(s); i++ {
		if s[i-1] > s[i] {
			return false
		}
	}
	return true
}

// TestNarrowed: the switch trips on any narrowed source, not just the first.
func TestNarrowed(t *testing.T) {
	whole := Source{Path: "/wt"}
	narrow := Source{Path: "/wt2", Include: []string{"/a/"}}
	if Narrowed(nil) || Narrowed([]Source{whole}) || Narrowed([]Source{whole, {Path: "/x", Include: []string{"**"}}}) {
		t.Error("Narrowed = true for sources that narrow nothing")
	}
	if !Narrowed([]Source{whole, narrow}) {
		t.Error("Narrowed = false with a narrowed source present")
	}
}

// TestCheckViewDirsEscapeBelt: ValidateInclude already rejects "..", so this
// path is only reachable if that validation is ever bypassed or weakened. It is
// the belt that guarantees no include-derived mount can point outside the
// worktree — a mount of /etc would be handed to the container verbatim.
func TestCheckViewDirsEscapeBelt(t *testing.T) {
	wt := t.TempDir()
	s := Source{RepoRoot: wt, Path: wt, Branch: "feat/x", Include: []string{"/../../etc/"}}
	err := checkViewDirs(s)
	if err == nil {
		t.Fatal("checkViewDirs accepted an include escaping the worktree")
	}
	if !strings.Contains(err.Error(), "escapes the worktree") {
		t.Errorf("error = %q, want it to name the escape", err)
	}
}

// TestCheckViewDirsRejectsAFile: a bind mount of a single file breaks atomic
// saves, so an include must name a directory that exists — a file is refused
// with the same message as a missing path.
func TestCheckViewDirsRejectsAFile(t *testing.T) {
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "go.mod"), []byte("module x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := Source{RepoRoot: wt, Path: wt, Branch: "feat/x", Include: []string{"/go.mod/"}}
	if err := checkViewDirs(s); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("checkViewDirs(file) = %v, want a not-a-directory refusal", err)
	}
}

// TestCheckViewDirsWholeRepoIsAlwaysFine: an unnarrowed source has no view
// directories to check.
func TestCheckViewDirsWholeRepoIsAlwaysFine(t *testing.T) {
	if err := checkViewDirs(Source{Path: "/does/not/exist"}); err != nil {
		t.Errorf("checkViewDirs(whole repo) = %v, want nil", err)
	}
}
