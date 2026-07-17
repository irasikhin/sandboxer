package sandbox

import (
	"io"
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

// TestMountFingerprint pins the inode-staleness guard for view mounts. A bind
// mount is pinned to the inode it names, so the fingerprint must be stable when
// nothing changed and flip the instant a mounted directory becomes a different
// inode — otherwise a live session keeps a mount orphaned by a host-side git
// checkout.
func TestMountFingerprint(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	for _, p := range []string{a, b} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// empty in → empty out: an unnarrowed sandbox's only mount is the
	// inode-stable <slug>/ root, which needs no fingerprint.
	if got := MountFingerprint(nil); got != "" {
		t.Errorf("MountFingerprint(nil) = %q, want empty", got)
	}

	base := MountFingerprint([]string{a, b})
	if base == "" {
		t.Fatal("MountFingerprint of real dirs is empty")
	}

	// stable across calls when nothing changed — the common re-enter path must
	// not spuriously rebuild the session.
	if again := MountFingerprint([]string{a, b}); again != base {
		t.Errorf("fingerprint changed with no filesystem change: %q → %q", base, again)
	}

	// writing a FILE inside a mounted dir does NOT change the dir's inode, so
	// the mount is still live and the fingerprint must not flip (the container
	// sees the new file through the existing mount).
	if err := os.WriteFile(filepath.Join(a, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := MountFingerprint([]string{a, b}); got != base {
		t.Errorf("fingerprint flipped on an in-dir write (inode unchanged): %q → %q", base, got)
	}

	// recreating a mounted dir (rmdir+mkdir, as a checkout switching branches
	// can) IS a new inode — the live mount is now orphaned, so the fingerprint
	// MUST flip to rebuild the session.
	if err := os.RemoveAll(a); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(a, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := MountFingerprint([]string{a, b}); got == base {
		t.Errorf("fingerprint did NOT flip after a mounted dir was recreated: %q", got)
	}

	// a vanished mount also flips the fingerprint (a missing dir the engine
	// would recreate root-owned must not read as unchanged).
	if err := os.RemoveAll(b); err != nil {
		t.Fatal(err)
	}
	if got := MountFingerprint([]string{a, b}); got == base {
		t.Errorf("fingerprint did NOT flip after a mount vanished: %q", got)
	}

	// order matters: the mounts are argv order (Mounts sorts them), so a
	// different order is a different argv and must fingerprint differently.
	if MountFingerprint([]string{a, b}) == MountFingerprint([]string{b, a}) {
		t.Error("fingerprint is order-insensitive, but the argv is ordered")
	}
}

// TestMountFingerprintStableAcrossSync is the property the whole guard depends
// on being cheap: a re-sync with no external change must NOT alter the view
// dirs' inodes, or every enter/exec would spuriously rebuild the session. And a
// host-side recreate of a view dir (a git checkout switching branches) MUST
// change the fingerprint, so the stale mount is caught.
func TestMountFingerprintStableAcrossSync(t *testing.T) {
	repo := gitRepoWithCommit(t)
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WriteProfileJSON("v", []byte(`{"srcs":[{"src":".","branch":"feat/v","include":["/serviceA/"]}]}`)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("v", io.Discard); err != nil {
		t.Fatal(err)
	}
	fp := func() string {
		_, m := Mounts(b.Srcs("v"))
		return MountFingerprint(m)
	}
	base := fp()
	if base == "" {
		t.Fatal("narrowed sandbox produced an empty mount fingerprint")
	}

	// re-sync with nothing changed → identical inode → identical fingerprint
	if _, err := b.SyncSrcs("v", io.Discard); err != nil {
		t.Fatal(err)
	}
	if again := fp(); again != base {
		t.Errorf("a no-op re-sync changed the fingerprint (%q → %q) — sessions would churn", base, again)
	}

	// simulate a host-side checkout recreating the view dir: rm + recreate
	viewDir := filepath.Join(b.Srcs("v")[0].Path, "serviceA")
	if err := os.RemoveAll(viewDir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(viewDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := fp(); got == base {
		t.Errorf("recreating the view dir did not flip the fingerprint (%q) — a live session would keep the orphaned mount", got)
	}
}
