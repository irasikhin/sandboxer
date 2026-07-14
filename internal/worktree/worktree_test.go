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

func TestIdentity(t *testing.T) {
	repo := gitRepo(t) // gitRepo sets user.name=t, user.email=t@example.com

	name, email := Identity(repo)
	if name != "t" || email != "t@example.com" {
		t.Errorf("Identity = %q/%q, want t/t@example.com", name, email)
	}

	// repo-local overrides win (as a host commit would resolve).
	git(t, repo, "config", "user.email", "local@x")
	if _, email := Identity(repo); email != "local@x" {
		t.Errorf("Identity email = %q, want local@x (local should win)", email)
	}
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

	// when: a full worktree (no includes)
	if err := Ensure(repo, dest, Branch("feat"), nil, nil); err != nil {
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
	if b := strings.TrimSpace(git(t, dest, "rev-parse", "--abbrev-ref", "HEAD")); b != "feat/feat-sb" {
		t.Errorf("branch = %q, want feat/feat-sb", b)
	}
	if !IsWorktree(dest) {
		t.Error("IsWorktree(dest) = false, want true")
	}
	if IsWorktree(t.TempDir()) {
		t.Error("IsWorktree(plain) = true, want false")
	}
}

func TestEnsureSparse(t *testing.T) {
	repo := gitRepo(t)
	dest := filepath.Join(t.TempDir(), "wt")

	// when: a worktree narrowed to serviceA
	if err := Ensure(repo, dest, Branch("narrow"), []string{"serviceA"}, nil); err != nil {
		t.Fatalf("Ensure sparse: %v", err)
	}

	// then: serviceA and root files present, serviceB/docs absent, status clean
	if _, err := os.Stat(filepath.Join(dest, "serviceA", "f.txt")); err != nil {
		t.Errorf("serviceA missing in sparse worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "CLAUDE.md")); err != nil {
		t.Errorf("root file missing in sparse worktree: %v", err)
	}
	for _, gone := range []string{"serviceB", "docs"} {
		if _, err := os.Stat(filepath.Join(dest, gone)); !os.IsNotExist(err) {
			t.Errorf("%s present in sparse worktree, want absent (err=%v)", gone, err)
		}
	}
	if s := git(t, dest, "status", "--porcelain"); strings.TrimSpace(s) != "" {
		t.Errorf("sparse worktree not clean:\n%s", s)
	}
	if l := strings.Fields(git(t, dest, "sparse-checkout", "list")); len(l) != 1 || l[0] != "serviceA" {
		t.Errorf("sparse-checkout list = %v, want [serviceA]", l)
	}
}

func TestEnsureReusesBranch(t *testing.T) {
	repo := gitRepo(t)
	dest := filepath.Join(t.TempDir(), "wt")
	branch := Branch("reuse")

	// given: a worktree with a commit made inside it
	if err := Ensure(repo, dest, branch, nil, nil); err != nil {
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
	if err := Ensure(repo, dest, branch, nil, nil); err != nil {
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
	branch := Branch("gone")
	if err := Ensure(repo, dest, branch, nil, nil); err != nil {
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

	// when: a full worktree with a progress writer
	var full strings.Builder
	if err := Ensure(repo, filepath.Join(t.TempDir(), "wt"), Branch("f"), nil, &full); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got := full.String(); !strings.Contains(got, "full repo") || !strings.Contains(got, "feat/f-sb") {
		t.Errorf("progress = %q, want it to mention the branch and 'full repo'", got)
	}

	// when: a sparse worktree with a progress writer → scope names the dirs
	var sparse strings.Builder
	if err := Ensure(repo, filepath.Join(t.TempDir(), "wt"), Branch("s"), []string{"serviceA"}, &sparse); err != nil {
		t.Fatalf("Ensure sparse: %v", err)
	}
	if got := sparse.String(); !strings.Contains(got, "serviceA") {
		t.Errorf("sparse progress = %q, want it to name serviceA", got)
	}
}

func TestEnsureErrorCarriesGitStderr(t *testing.T) {
	repo := gitRepo(t)
	dest := filepath.Join(t.TempDir(), "wt")
	if err := Ensure(repo, dest, Branch("dup"), nil, nil); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}

	// when: a second Ensure onto the same dest → git fails, error is wrapped
	err := Ensure(repo, dest, Branch("dup"), nil, nil)
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
	if err := DeleteBranch(repo, Branch("nope")); err == nil {
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
