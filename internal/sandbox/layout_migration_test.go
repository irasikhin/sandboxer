package sandbox

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/worktree"
)

// TestSyncSrcsRelocatesOldLayout: a sandbox recorded under the pre-flip
// repo-first layout (<slug>/<repo>/<branch>) is MOVED to the branch-first
// path on the next sync — one same-filesystem rename, so uncommitted work
// AND git-ignored build caches survive (a detach-and-recheckout would
// destroy the caches: git considers an ignored-only tree clean).
func TestSyncSrcsRelocatesOldLayout(t *testing.T) {
	repo := gitRepoWithCommit(t)
	writeFile(t, filepath.Join(repo, ".gitignore"), "cache/\n")
	runGit(t, repo, "add", ".gitignore")
	runGit(t, repo, "commit", "-qm", "ignore cache")
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WriteProfileJSON("old", []byte(`{"srcs":[{"src":".","branch":"feat/old"}]}`)); err != nil {
		t.Fatal(err)
	}

	// given: the worktree sits at the OLD repo-first path, recorded as such
	oldPath := filepath.Join(b.SandboxDir("old"), filepath.Base(repo), "feat", "old")
	if err := worktree.Ensure(repo, oldPath, "feat/old", io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := b.writeSrcs("old", []Source{
		{RepoRoot: repo, Path: oldPath, Branch: "feat/old", Managed: true, AutoBranch: true},
	}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(oldPath, "wip.txt"), "precious")
	writeFile(t, filepath.Join(oldPath, "cache", "blob.bin"), "expensive to rebuild")

	// when: the branch-first sandboxer syncs
	var progress strings.Builder
	srcs, err := b.SyncSrcs("old", &progress)
	if err != nil {
		t.Fatal(err)
	}

	// then: the worktree moved to <slug>/<branch>/<repo> with everything inside
	want := filepath.Join(b.SandboxDir("old"), "feat", "old", filepath.Base(repo))
	if len(srcs) != 1 || srcs[0].Path != want || !srcs[0].AutoBranch {
		t.Fatalf("srcs = %+v, want the single source at %q", srcs, want)
	}
	for _, f := range []string{"wip.txt", filepath.Join("cache", "blob.bin")} {
		if _, err := os.Stat(filepath.Join(want, f)); err != nil {
			t.Errorf("%s did not survive the relocation: %v", f, err)
		}
	}
	if !strings.Contains(progress.String(), "moved") {
		t.Errorf("expected a moved notice, got %q", progress.String())
	}
	// and: git tracks the new location, the old parents are tidied away, and
	// nothing went through _detached/
	if wt, ok := worktree.FindWorktree(repo, "feat/old"); !ok || wt != want {
		t.Errorf("git worktree for feat/old = %q ok=%v, want %q", wt, ok, want)
	}
	if _, err := os.Stat(filepath.Join(b.SandboxDir("old"), filepath.Base(repo))); !os.IsNotExist(err) {
		t.Errorf("old repo-first dir survived (err=%v)", err)
	}
	if _, err := os.Stat(b.detachedDir("old")); !os.IsNotExist(err) {
		t.Errorf("relocation went through _detached (err=%v)", err)
	}

	// and: the layout is converged — a second sync moves nothing
	progress.Reset()
	if _, err := b.SyncSrcs("old", &progress); err != nil {
		t.Fatal(err)
	}
	if s := progress.String(); strings.Contains(s, "moved") || strings.Contains(s, "aside") {
		t.Errorf("second sync relocated again: %q", s)
	}
}

// TestSyncSrcsRelocatesDetachedHead: a detached-HEAD tree (a rebase stop, a
// pinned commit) at an outdated managed path MOVES with everyone else —
// "unknown" is not "switched", and leaving it at the old path would wedge the
// sync: git still binds the branch to that checkout, so Ensure at the new
// path would fail its one-worktree-per-branch rule.
func TestSyncSrcsRelocatesDetachedHead(t *testing.T) {
	repo := gitRepoWithCommit(t)
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WriteProfileJSON("det", []byte(`{"srcs":[{"src":".","branch":"feat/det"}]}`)); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(b.SandboxDir("det"), filepath.Base(repo), "feat", "det")
	if err := worktree.Ensure(repo, oldPath, "feat/det", io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := b.writeSrcs("det", []Source{
		{RepoRoot: repo, Path: oldPath, Branch: "feat/det", Managed: true},
	}); err != nil {
		t.Fatal(err)
	}
	runGit(t, oldPath, "checkout", "-q", "--detach")
	writeFile(t, filepath.Join(oldPath, "wip.txt"), "precious")

	srcs, err := b.SyncSrcs("det", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(b.SandboxDir("det"), "feat", "det", filepath.Base(repo))
	if len(srcs) != 1 || srcs[0].Path != want {
		t.Fatalf("srcs = %+v, want the single source at %q", srcs, want)
	}
	if _, err := os.Stat(filepath.Join(want, "wip.txt")); err != nil {
		t.Errorf("mid-rebase work did not move with the worktree: %v", err)
	}
	if _, err := os.Stat(b.detachedDir("det")); !os.IsNotExist(err) {
		t.Errorf("detached-HEAD tree was set aside instead of moved (err=%v)", err)
	}
}

// TestSyncSrcsRelocateFallbackDetach: crossed repo/branch names make the two
// relocation targets each other's still-occupied OLD paths (repo "api" on
// branch "feat" and repo "feat" on branch "api" swap places under the flip).
// The first move fails and must fall back to detach — never abort the sync —
// after which the freed paths let everything converge, dirty work included
// (it returns from _detached/ via materializeSrc's re-attach).
func TestSyncSrcsRelocateFallbackDetach(t *testing.T) {
	dir := t.TempDir()
	repoA := gitRepoAt(t, filepath.Join(dir, "api"))
	repoB := gitRepoAt(t, filepath.Join(dir, "feat"))
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pj := `{"srcs":[{"src":"` + repoA + `","branch":"feat"},{"src":"` + repoB + `","branch":"api"}]}`
	if err := b.WriteProfileJSON("x", []byte(pj)); err != nil {
		t.Fatal(err)
	}

	// given: both worktrees at their OLD repo-first paths — each one exactly
	// where the OTHER belongs under branch-first
	oldA := filepath.Join(b.SandboxDir("x"), "api", "feat") // new home of B
	oldB := filepath.Join(b.SandboxDir("x"), "feat", "api") // new home of A
	if err := worktree.Ensure(repoA, oldA, "feat", io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := worktree.Ensure(repoB, oldB, "api", io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := b.writeSrcs("x", []Source{
		{RepoRoot: repoA, Path: oldA, Branch: "feat", Managed: true},
		{RepoRoot: repoB, Path: oldB, Branch: "api", Managed: true},
	}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(oldA, "wip.txt"), "precious")

	// when: the branch-first sandboxer syncs
	var progress strings.Builder
	srcs, err := b.SyncSrcs("x", &progress)
	if err != nil {
		t.Fatalf("SyncSrcs must fall back to detach, not fail: %v", err)
	}

	// then: both sources converged onto their swapped branch-first paths, the
	// dirty tree's work intact after its _detached/ round-trip
	if len(srcs) != 2 || srcs[0].Path != oldB || srcs[1].Path != oldA {
		t.Fatalf("srcs = %+v, want %q and %q", srcs, oldB, oldA)
	}
	if _, err := os.Stat(filepath.Join(oldB, "wip.txt")); err != nil {
		t.Errorf("uncommitted work lost in the fallback: %v", err)
	}
	for _, msg := range []string{"setting the worktree aside", "re-attached"} {
		if !strings.Contains(progress.String(), msg) {
			t.Errorf("progress misses %q:\n%s", msg, progress.String())
		}
	}
	if wt, ok := worktree.FindWorktree(repoA, "feat"); !ok || wt != oldB {
		t.Errorf("repo api worktree = %q ok=%v, want %q", wt, ok, oldB)
	}
	if wt, ok := worktree.FindWorktree(repoB, "api"); !ok || wt != oldA {
		t.Errorf("repo feat worktree = %q ok=%v, want %q", wt, ok, oldA)
	}
	if _, err := os.Stat(b.detachedDir("x")); !os.IsNotExist(err) {
		t.Errorf("_detached survived the converged sync (err=%v)", err)
	}
}

// TestAssignManagedPathsRejectsNesting: branch dirs of different repos share
// the <slug>/ namespace, so a repo leaf can collide with another source's
// branch path — repo "x" on branch "feat" is <slug>/feat/x, and any repo on
// branch "feat/x" would nest INSIDE that worktree (git creates it happily;
// removing the outer tree would take the inner with it). Refused up front,
// in both directions.
func TestAssignManagedPathsRejectsNesting(t *testing.T) {
	for _, srcs := range [][]Source{
		{
			{RepoRoot: "/r/x", Branch: "feat", Managed: true},
			{RepoRoot: "/r/b", Branch: "feat/x", Managed: true},
		},
		{
			{RepoRoot: "/r/b", Branch: "feat/x", Managed: true},
			{RepoRoot: "/r/x", Branch: "feat", Managed: true},
		},
	} {
		err := assignManagedPaths("/sb/slug", srcs)
		if err == nil || !strings.Contains(err.Error(), "collide") {
			t.Fatalf("assignManagedPaths(%+v) = %v, want a collision error", srcs, err)
		}
	}
}
