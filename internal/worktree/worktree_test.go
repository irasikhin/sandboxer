package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireGit skips a test when git is not on PATH (Detect's own fallback makes
// the package usable without it, but these tests drive git directly).
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

// gitRepo creates a temp repository with a few directories, a root-level file
// and one commit, and returns its path.
func gitRepo(t *testing.T) string {
	t.Helper()
	requireGit(t)
	repo := t.TempDir()
	git(t, repo, "init", "-q")
	git(t, repo, "config", "user.email", "t@example.com")
	git(t, repo, "config", "user.name", "t")
	for _, d := range []string{"serviceA", "serviceB", "docs"} {
		if err := os.MkdirAll(filepath.Join(repo, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, d, "f.txt"), []byte(d), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte("ctx"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-qm", "init")
	return repo
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := run(dir, args...)
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return out
}

func TestDetect(t *testing.T) {
	repo := gitRepo(t)

	// given: a git repo with a commit → detected, absolute paths
	top, common, ok := Detect(repo)
	if !ok {
		t.Fatal("Detect(repo) ok=false, want true")
	}
	if !filepath.IsAbs(top) || !filepath.IsAbs(common) {
		t.Errorf("Detect paths not absolute: top=%q common=%q", top, common)
	}
	// EvalSymlinks because macOS/tmp may be symlinked; compare resolved.
	if got, want := mustEval(t, top), mustEval(t, repo); got != want {
		t.Errorf("toplevel = %q, want %q", got, want)
	}

	// when: called from a subdirectory → same toplevel
	sub := filepath.Join(repo, "serviceA")
	subTop, _, ok := Detect(sub)
	if !ok || mustEval(t, subTop) != mustEval(t, repo) {
		t.Errorf("Detect(subdir) top=%q ok=%v, want repo root", subTop, ok)
	}

	// when: a plain non-git dir → not detected
	if _, _, ok := Detect(t.TempDir()); ok {
		t.Error("Detect(non-git) ok=true, want false")
	}

	// when: a fresh repo with no commit → not detected (nothing to branch from)
	empty := t.TempDir()
	git(t, empty, "init", "-q")
	if _, _, ok := Detect(empty); ok {
		t.Error("Detect(no-HEAD repo) ok=true, want false")
	}
}

func TestEnsureFull(t *testing.T) {
	repo := gitRepo(t)
	dest := filepath.Join(t.TempDir(), "wt")

	// when: a worktree is created
	if err := Ensure(repo, dest, "feat/feat", nil); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	// then: every directory and the root file are present
	for _, p := range []string{"serviceA", "serviceB", "docs", "CLAUDE.md"} {
		if _, err := os.Stat(filepath.Join(dest, p)); err != nil {
			t.Errorf("missing %s in full worktree: %v", p, err)
		}
	}
	// then: it is a clean worktree on the sandbox branch
	if s := git(t, dest, "status", "--porcelain"); strings.TrimSpace(s) != "" {
		t.Errorf("worktree not clean:\n%s", s)
	}
	if b := strings.TrimSpace(git(t, dest, "rev-parse", "--abbrev-ref", "HEAD")); b != "feat/feat" {
		t.Errorf("branch = %q, want feat/feat", b)
	}
	if !IsWorktree(dest) {
		t.Error("IsWorktree(dest) = false, want true")
	}
	if IsWorktree(t.TempDir()) {
		t.Error("IsWorktree(plain) = true, want false")
	}
	// A STANDALONE repo (.git is a real directory) is not a linked worktree —
	// the live case is an in-guest `git init` over the pointer file; `git
	// worktree move/remove` refuse such a dir, so IsWorktree must not claim it.
	standalone := t.TempDir()
	git(t, standalone, "init", "-q")
	if IsWorktree(standalone) {
		t.Error("IsWorktree(standalone repo) = true, want false")
	}
}

// TestUnsparseWidensALegacyWorktree is the upgrade path: a worktree a
// pre-view-mounts sandboxer narrowed with sparse-checkout is missing files on
// the HOST, which is exactly what stops an IDE from opening it. Unsparse widens
// it in place, and the uncommitted work in it survives — the sandbox is never
// recreated for this.
func TestUnsparseWidensALegacyWorktree(t *testing.T) {
	repo := gitRepo(t)
	dest := filepath.Join(t.TempDir(), "wt")
	if err := Ensure(repo, dest, "feat/legacy", nil); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// given: a tree narrowed the way the old sandboxer did it
	git(t, dest, "sparse-checkout", "set", "--no-cone", "--", "/serviceA/")
	if _, err := os.Stat(filepath.Join(dest, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Fatalf("setup: CLAUDE.md should be sparsed out, err=%v", err)
	}
	// given: uncommitted work inside the surviving selection
	work := filepath.Join(dest, "serviceA", "wip.txt")
	if err := os.WriteFile(work, []byte("in progress"), 0o644); err != nil {
		t.Fatal(err)
	}

	// when
	var progress strings.Builder
	changed, err := Unsparse(dest, &progress)
	if err != nil || !changed {
		t.Fatalf("Unsparse = (%v, %v), want (true, nil)", changed, err)
	}
	// then: the widening is announced — a tree silently growing files would be
	// baffling mid-session.
	if got := progress.String(); !strings.Contains(got, "widened") {
		t.Errorf("progress = %q, want it to report the widening", got)
	}

	// then: the host tree is whole again
	for _, p := range []string{"serviceA", "serviceB", "docs", "CLAUDE.md"} {
		if _, err := os.Stat(filepath.Join(dest, p)); err != nil {
			t.Errorf("Unsparse did not restore %s: %v", p, err)
		}
	}
	// then: the work is untouched
	if b, err := os.ReadFile(work); err != nil || string(b) != "in progress" {
		t.Errorf("uncommitted work lost: %q, %v", b, err)
	}
	if got := sparseList(dest); got != nil {
		t.Errorf("sparse-checkout still active: %v", got)
	}
	// then: idempotent — the common case is an already-full tree
	if changed, err := Unsparse(dest, nil); err != nil || changed {
		t.Errorf("Unsparse(again) = (%v, %v), want no-op", changed, err)
	}
}

// TestFindWorktree locates the checkout of a branch — the main checkout or an
// added worktree — and reports absence for a branch checked out nowhere.
func TestFindWorktree(t *testing.T) {
	repo := gitRepo(t)
	main := strings.TrimSpace(git(t, repo, "rev-parse", "--abbrev-ref", "HEAD"))
	if p, ok := FindWorktree(repo, main); !ok || p != repo {
		t.Errorf("FindWorktree(main) = (%q, %v), want the main checkout", p, ok)
	}
	dest := filepath.Join(t.TempDir(), "wt")
	if err := Ensure(repo, dest, "feat/found", nil); err != nil {
		t.Fatal(err)
	}
	if p, ok := FindWorktree(repo, "feat/found"); !ok || p != dest {
		t.Errorf("FindWorktree(added) = (%q, %v), want %q", p, ok, dest)
	}
	if _, ok := FindWorktree(repo, "no/such-branch"); ok {
		t.Error("FindWorktree(missing) = ok, want false")
	}
	if CurrentBranch(dest) != "feat/found" {
		t.Errorf("CurrentBranch = %q, want %q", CurrentBranch(dest), "feat/found")
	}
}

// TestMove relocates a worktree with uncommitted work intact (the _detached
// path for dropped sources).
func TestMove(t *testing.T) {
	repo := gitRepo(t)
	from := filepath.Join(t.TempDir(), "wt")
	if err := Ensure(repo, from, "feat/mv", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(from, "wip.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	to := filepath.Join(t.TempDir(), "moved")
	if err := Move(repo, from, to); err != nil {
		t.Fatalf("Move: %v", err)
	}
	if _, err := os.Stat(filepath.Join(to, "wip.txt")); err != nil {
		t.Errorf("uncommitted work lost in Move: %v", err)
	}
	if !IsWorktree(to) {
		t.Error("moved dir is no longer a tracked worktree")
	}
}

// TestHasWork: clean = removable; an untracked file, a tracked modification or
// an unreadable state all read as "has work" (preserve, never destroy).
func TestHasWork(t *testing.T) {
	repo := gitRepo(t)
	dest := filepath.Join(t.TempDir(), "wt")
	if err := Ensure(repo, dest, "feat/hw", nil); err != nil {
		t.Fatal(err)
	}
	if HasWork(dest) {
		t.Error("fresh worktree reads as having work, want clean")
	}
	if err := os.WriteFile(filepath.Join(dest, "wip.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !HasWork(dest) {
		t.Error("untracked file not seen as work")
	}
	if err := os.Remove(filepath.Join(dest, "wip.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "CLAUDE.md"), []byte("edited"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !HasWork(dest) {
		t.Error("tracked modification not seen as work")
	}
	if !HasWork(t.TempDir()) {
		t.Error("a non-repo dir must read as having work (unknown = preserve)")
	}
}

func TestEnsureReusesBranch(t *testing.T) {
	repo := gitRepo(t)
	dest := filepath.Join(t.TempDir(), "wt")
	branch := "feat/reuse"

	// given: a worktree with a commit made inside it
	if err := Ensure(repo, dest, branch, nil); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dest, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dest, "add", "new.txt")
	git(t, dest, "commit", "-qm", "agent work")

	// when: the worktree is torn down (branch kept) and re-created
	if err := Remove(repo, dest); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("dest still present after Remove: %v", err)
	}
	if err := Ensure(repo, dest, branch, nil); err != nil {
		t.Fatalf("re-Ensure: %v", err)
	}

	// then: the agent's commit survived (branch was reused, not recreated)
	if _, err := os.Stat(filepath.Join(dest, "new.txt")); err != nil {
		t.Errorf("agent commit lost on recreate: %v", err)
	}
}

func TestRemoveAndDeleteBranch(t *testing.T) {
	repo := gitRepo(t)
	dest := filepath.Join(t.TempDir(), "wt")
	branch := "feat/gone"
	if err := Ensure(repo, dest, branch, nil); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	// when: removed → worktree gone from git, branch still present
	if err := Remove(repo, dest); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if strings.Contains(git(t, repo, "worktree", "list"), dest) {
		t.Error("worktree still listed after Remove")
	}
	if !strings.Contains(git(t, repo, "branch", "--list", branch), branch) {
		t.Error("branch deleted by Remove, want kept")
	}

	// when: branch explicitly deleted → gone
	if err := DeleteBranch(repo, branch); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
	if strings.TrimSpace(git(t, repo, "branch", "--list", branch)) != "" {
		t.Error("branch still present after DeleteBranch")
	}
}

func TestEnsureReportsProgress(t *testing.T) {
	repo := gitRepo(t)

	// when: a worktree is created with a progress writer
	var full strings.Builder
	if err := Ensure(repo, filepath.Join(t.TempDir(), "wt"), "feat/f", &full); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got := full.String(); !strings.Contains(got, "full repo") || !strings.Contains(got, "feat/f") {
		t.Errorf("progress = %q, want it to mention the branch and 'full repo'", got)
	}
}

func TestEnsureErrorCarriesGitStderr(t *testing.T) {
	repo := gitRepo(t)
	dest := filepath.Join(t.TempDir(), "wt")
	if err := Ensure(repo, dest, "feat/dup", nil); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}

	// when: a second Ensure onto the same dest → git fails, error is wrapped
	err := Ensure(repo, dest, "feat/dup", nil)
	if err == nil {
		t.Fatal("second Ensure onto existing dest = nil, want error")
	}
	if !strings.Contains(err.Error(), "git worktree add") {
		t.Errorf("error = %q, want it wrapped with 'git worktree add'", err)
	}
}

func TestRemoveNonWorktreeAndPrune(t *testing.T) {
	repo := gitRepo(t)

	// given: a plain directory that git does not track as a worktree
	plain := filepath.Join(t.TempDir(), "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}

	// when: Remove is asked to tear it down → best-effort deletes it anyway
	if err := Remove(repo, plain); err != nil {
		t.Fatalf("Remove(non-worktree): %v", err)
	}
	if _, err := os.Stat(plain); !os.IsNotExist(err) {
		t.Errorf("plain dir survived Remove: %v", err)
	}

	// and: Prune on the repo is a clean no-op
	if err := Prune(repo); err != nil {
		t.Errorf("Prune: %v", err)
	}
}

func TestDeleteMissingBranchErrors(t *testing.T) {
	repo := gitRepo(t)
	// when: deleting a branch that does not exist → error surfaced
	if err := DeleteBranch(repo, "feat/nope"); err == nil {
		t.Error("DeleteBranch(missing) = nil, want error")
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return r
}
