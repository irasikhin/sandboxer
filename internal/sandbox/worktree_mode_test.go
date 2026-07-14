package sandbox

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// gitRepoWithCommit builds a temp repo (serviceA, serviceB, a root file) with a
// single commit, so ResolveBase detects it and MakeSandbox can branch off HEAD.
func gitRepoWithCommit(t *testing.T) string {
	t.Helper()
	requireGit(t)
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.email", "t@example.com")
	runGit(t, repo, "config", "user.name", "t")
	runGit(t, repo, "config", "commit.gpgsign", "false")
	for _, d := range []string{"serviceA", "serviceB"} {
		writeFile(t, filepath.Join(repo, d, "f.txt"), d)
	}
	writeFile(t, filepath.Join(repo, "CLAUDE.md"), "ctx")
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-qm", "init")
	return repo
}

func TestMakeSandboxGitModeFull(t *testing.T) {
	repo := gitRepoWithCommit(t)

	// given: a base resolved on a git repo → worktree mode detected
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatalf("ResolveBase: %v", err)
	}
	if b.RepoRoot == "" || b.GitDir == "" {
		t.Fatalf("git repo not detected: RepoRoot=%q GitDir=%q", b.RepoRoot, b.GitDir)
	}

	// when: a sandbox is made → a full worktree on branch sandbox/<slug>
	if err := b.MakeSandbox("wt", io.Discard); err != nil {
		t.Fatalf("MakeSandbox: %v", err)
	}
	dest := b.SandboxDir("wt")

	// then: every dir + root file + .git present, on the branch, clean
	for _, p := range []string{"serviceA", "serviceB", "CLAUDE.md", ".git"} {
		if _, err := os.Stat(filepath.Join(dest, p)); err != nil {
			t.Errorf("missing %s in worktree: %v", p, err)
		}
	}
	if br := strings.TrimSpace(runGit(t, dest, "rev-parse", "--abbrev-ref", "HEAD")); br != "feat/wt-sb" {
		t.Errorf("branch = %q, want feat/wt-sb", br)
	}
	if s := runGit(t, dest, "status", "--porcelain"); strings.TrimSpace(s) != "" {
		t.Errorf("worktree not clean:\n%s", s)
	}
	// and: no copy-mode workspace/ dir is created in git mode
	if _, err := os.Stat(filepath.Join(dest, "workspace")); !os.IsNotExist(err) {
		t.Errorf("unexpected workspace/ dir in git-mode sandbox (err=%v)", err)
	}
}

func TestMakeSandboxGitModeSparse(t *testing.T) {
	repo := gitRepoWithCommit(t)
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}

	// given: a stored profile that narrows to serviceA
	if err := b.WriteProfileJSON("narrow", []byte(`{"deps":["serviceA"]}`)); err != nil {
		t.Fatal(err)
	}

	// when: the sandbox is made → sparse-checkout to serviceA only
	if err := b.MakeSandbox("narrow", io.Discard); err != nil {
		t.Fatalf("MakeSandbox sparse: %v", err)
	}
	dest := b.SandboxDir("narrow")

	// then: serviceA present, serviceB excluded, status clean
	if _, err := os.Stat(filepath.Join(dest, "serviceA", "f.txt")); err != nil {
		t.Errorf("serviceA missing in sparse worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "serviceB")); !os.IsNotExist(err) {
		t.Errorf("serviceB present, want sparse-excluded (err=%v)", err)
	}
	if s := runGit(t, dest, "status", "--porcelain"); strings.TrimSpace(s) != "" {
		t.Errorf("sparse worktree not clean:\n%s", s)
	}
}

func TestRemoveStateGitModeKeepsBranch(t *testing.T) {
	repo := gitRepoWithCommit(t)
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("gone", io.Discard); err != nil {
		t.Fatal(err)
	}
	dest := b.SandboxDir("gone")

	// when: state is removed → worktree gone from disk and from git, branch kept
	b.RemoveState("gone", true)
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("sandbox dir survived RemoveState: %v", err)
	}
	if strings.Contains(runGit(t, repo, "worktree", "list"), dest) {
		t.Error("worktree still listed after RemoveState")
	}
	if !strings.Contains(runGit(t, repo, "branch", "--list", "feat/gone-sb"), "feat/gone-sb") {
		t.Error("branch removed by RemoveState, want kept")
	}

	// and: RemoveSandboxBranch then deletes it (recreate --full path)
	b.RemoveSandboxBranch("gone")
	if strings.TrimSpace(runGit(t, repo, "branch", "--list", "feat/gone-sb")) != "" {
		t.Error("branch present after RemoveSandboxBranch")
	}
}
