package sandbox

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/worktree"
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
// single commit, so a srcs entry can resolve it and branch off HEAD.
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

func TestMakeSandboxDotSrcFull(t *testing.T) {
	repo := gitRepoWithCommit(t)
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatalf("ResolveBase: %v", err)
	}

	// when: a sandbox is made from the scaffold-style explicit {src: "."}
	if err := b.WriteProfileJSON("wt", []byte(`{"srcs":[{"src":"."}]}`)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("wt", io.Discard); err != nil {
		t.Fatalf("MakeSandbox: %v", err)
	}
	srcs := b.Srcs("wt")
	if len(srcs) != 1 {
		t.Fatalf("srcs = %+v, want one source", srcs)
	}
	s := srcs[0]
	if !s.Managed || !s.AutoBranch || s.Branch != "feat/wt-sb" || s.RepoRoot != repo {
		t.Errorf("dot source wrong: %+v", s)
	}
	// then: the worktree lives UNDER <slug>/ (the sandbox dir is not itself a
	// worktree), with every file present and clean, on the sandbox branch.
	if s.Path == b.SandboxDir("wt") || filepath.Dir(s.Path) != b.SandboxDir("wt") {
		t.Fatalf("worktree path %q not nested under %q", s.Path, b.SandboxDir("wt"))
	}
	if worktree.IsWorktree(b.SandboxDir("wt")) {
		t.Error("sandbox dir itself must not be a worktree")
	}
	for _, p := range []string{"serviceA", "serviceB", "CLAUDE.md", ".git"} {
		if _, err := os.Stat(filepath.Join(s.Path, p)); err != nil {
			t.Errorf("missing %s in worktree: %v", p, err)
		}
	}
	if br := strings.TrimSpace(runGit(t, s.Path, "rev-parse", "--abbrev-ref", "HEAD")); br != "feat/wt-sb" {
		t.Errorf("branch = %q, want feat/wt-sb", br)
	}
	if st := runGit(t, s.Path, "status", "--porcelain"); strings.TrimSpace(st) != "" {
		t.Errorf("worktree not clean:\n%s", st)
	}
	// and: nothing extra is mounted for a fully managed sandbox
	if m := SrcMounts(srcs); len(m) != 0 {
		t.Errorf("SrcMounts = %v, want none (managed lives under <slug>/)", m)
	}
}

func TestMakeSandboxSparseInclude(t *testing.T) {
	repo := gitRepoWithCommit(t)
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}

	// given: a stored profile narrowing to serviceA via a gitignore pattern
	if err := b.WriteProfileJSON("narrow", []byte(`{"srcs":[{"src":".","include":["/serviceA/"]}]}`)); err != nil {
		t.Fatal(err)
	}

	if err := b.MakeSandbox("narrow", io.Discard); err != nil {
		t.Fatalf("MakeSandbox sparse: %v", err)
	}
	s := b.Srcs("narrow")[0]

	// then: ONLY the included path is materialized — non-cone sparse keeps no
	// root files, so the container (which mounts the worktree contents) sees
	// nothing but the selection.
	if _, err := os.Stat(filepath.Join(s.Path, "serviceA", "f.txt")); err != nil {
		t.Errorf("serviceA missing in sparse worktree: %v", err)
	}
	for _, gone := range []string{"serviceB", "CLAUDE.md"} {
		if _, err := os.Stat(filepath.Join(s.Path, gone)); !os.IsNotExist(err) {
			t.Errorf("%s present, want sparse-excluded (err=%v)", gone, err)
		}
	}
	if st := runGit(t, s.Path, "status", "--porcelain"); strings.TrimSpace(st) != "" {
		t.Errorf("sparse worktree not clean:\n%s", st)
	}
}

// TestSyncSrcsLiveRefresh: editing srcs and re-syncing converges the existing
// sandbox in place — a widened include re-materializes files, a second repo
// appears under <slug>/, and a dropped source is set aside under _detached/
// with its work intact (never destroyed, never left visible).
func TestSyncSrcsLiveRefresh(t *testing.T) {
	repo := gitRepoWithCommit(t)
	other := gitRepoWithCommit(t)
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WriteProfileJSON("live", []byte(`{"srcs":[{"src":".","include":["/serviceA/"]}]}`)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("live", io.Discard); err != nil {
		t.Fatal(err)
	}
	first := b.Srcs("live")[0]

	// when: the profile widens the include and adds a second repo
	pj := fmt.Sprintf(`{"srcs":[{"src":"."},{"src":%q}]}`, other)
	if err := b.WriteProfileJSON("live", []byte(pj)); err != nil {
		t.Fatal(err)
	}
	srcs, err := b.SyncSrcs("live", io.Discard)
	if err != nil {
		t.Fatalf("SyncSrcs: %v", err)
	}
	if len(srcs) != 2 {
		t.Fatalf("srcs = %+v, want 2", srcs)
	}
	// then: the first worktree is widened in place (same path), serviceB now on disk
	if srcs[0].Path != first.Path {
		t.Errorf("first source moved: %q → %q", first.Path, srcs[0].Path)
	}
	if _, err := os.Stat(filepath.Join(srcs[0].Path, "serviceB", "f.txt")); err != nil {
		t.Errorf("widened include did not materialize serviceB: %v", err)
	}
	// and: the second repo appeared under <slug>/ on its own worktree
	if _, err := os.Stat(filepath.Join(srcs[1].Path, "CLAUDE.md")); err != nil {
		t.Errorf("second source not materialized: %v", err)
	}
	if filepath.Dir(srcs[1].Path) != b.SandboxDir("live") {
		t.Errorf("second source %q not under the sandbox dir", srcs[1].Path)
	}

	// when: the second repo is dropped again, with uncommitted work inside
	writeFile(t, filepath.Join(srcs[1].Path, "wip.txt"), "precious")
	if err := b.WriteProfileJSON("live", []byte(`{"srcs":[{"src":"."}]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SyncSrcs("live", io.Discard); err != nil {
		t.Fatalf("SyncSrcs (drop): %v", err)
	}
	// then: its worktree left <slug>/ (no longer visible in the container) but
	// the uncommitted file survived under _detached/.
	if _, err := os.Stat(srcs[1].Path); !os.IsNotExist(err) {
		t.Errorf("dropped source still under the sandbox dir (err=%v)", err)
	}
	moved, err := filepath.Glob(filepath.Join(b.detachedDir(), "live-*", "wip.txt"))
	if err != nil || len(moved) != 1 {
		t.Errorf("dropped source's work not preserved under _detached: %v (err=%v)", moved, err)
	}
}

// TestSrcsAdoptExistingWorktree: a srcs entry with branch: adopts the checkout
// that already has that branch — here the repo's main checkout itself — and
// surfaces as an extra (unmanaged) mount instead of a managed worktree.
func TestSrcsAdoptExistingWorktree(t *testing.T) {
	repo := gitRepoWithCommit(t)
	project := t.TempDir()
	b, err := ResolveBase(project)
	if err != nil {
		t.Fatal(err)
	}
	branch := strings.TrimSpace(runGit(t, repo, "rev-parse", "--abbrev-ref", "HEAD"))
	pj := fmt.Sprintf(`{"srcs":[{"src":%q,"branch":%q,"include":["/serviceA/"]}]}`, repo, branch)
	if err := b.WriteProfileJSON("adopt", []byte(pj)); err != nil {
		t.Fatal(err)
	}

	var progress strings.Builder
	if err := b.MakeSandbox("adopt", &progress); err != nil {
		t.Fatalf("MakeSandbox: %v", err)
	}
	srcs := b.Srcs("adopt")
	if len(srcs) != 1 || srcs[0].Managed || srcs[0].Path != repo {
		t.Fatalf("adopted source wrong: %+v", srcs)
	}
	// include on an adopted worktree is ignored (its sparse state is the
	// user's), with a notice; the checkout is untouched.
	if !strings.Contains(progress.String(), "include ignored") {
		t.Errorf("expected an include-ignored notice, got %q", progress.String())
	}
	if _, err := os.Stat(filepath.Join(repo, "serviceB", "f.txt")); err != nil {
		t.Errorf("adopted checkout was narrowed: %v", err)
	}
	if m := SrcMounts(srcs); len(m) != 1 || m[0] != repo {
		t.Errorf("SrcMounts = %v, want the adopted path", m)
	}
	// teardown never touches an adopted worktree
	b.RemoveState("adopt", false)
	if _, err := os.Stat(filepath.Join(repo, "serviceA", "f.txt")); err != nil {
		t.Errorf("RemoveState touched the adopted worktree: %v", err)
	}
}

func TestResolveSrcsErrors(t *testing.T) {
	repo := gitRepoWithCommit(t)
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	nonGit := t.TempDir()
	cases := []struct {
		name, pj, errPart string
	}{
		{"non-git", fmt.Sprintf(`{"srcs":[{"src":%q}]}`, nonGit), "not a git repository"},
		{"missing", `{"srcs":[{"src":"./no-such-dir"}]}`, "no such directory"},
		{"subdir", `{"srcs":[{"src":"./serviceA"}]}`, "repository root"},
		{"dup", fmt.Sprintf(`{"srcs":[{"src":"."},{"src":%q}]}`, repo), "listed twice"},
		{"empty", `{"srcs":[{"src":""}]}`, "empty src"},
		{"no-srcs", `{}`, "srcs is empty"},
	}
	for _, c := range cases {
		if err := b.WriteProfileJSON("bad", []byte(c.pj)); err != nil {
			t.Fatal(err)
		}
		_, err := b.SyncSrcs("bad", io.Discard)
		if err == nil || !strings.Contains(err.Error(), c.errPart) {
			t.Errorf("%s: SyncSrcs = %v, want %q", c.name, err, c.errPart)
		}
	}
}

func TestRemoveStateKeepsBranchesFullDropsAuto(t *testing.T) {
	repo := gitRepoWithCommit(t)
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WriteProfileJSON("gone", []byte(`{"srcs":[{"src":"."}]}`)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("gone", io.Discard); err != nil {
		t.Fatal(err)
	}
	srcs := b.Srcs("gone") // captured before RemoveState wipes the meta
	wt := srcs[0].Path

	// when: state is removed → worktree gone from disk and from git, branch kept
	b.RemoveState("gone", true)
	if _, err := os.Stat(b.SandboxDir("gone")); !os.IsNotExist(err) {
		t.Errorf("sandbox dir survived RemoveState: %v", err)
	}
	if strings.Contains(runGit(t, repo, "worktree", "list"), wt) {
		t.Error("worktree still listed after RemoveState")
	}
	if !strings.Contains(runGit(t, repo, "branch", "--list", "feat/gone-sb"), "feat/gone-sb") {
		t.Error("branch removed by RemoveState, want kept")
	}

	// and: RemoveSandboxBranches then deletes the auto-named one (recreate --full)
	b.RemoveSandboxBranches("gone", srcs)
	if strings.TrimSpace(runGit(t, repo, "branch", "--list", "feat/gone-sb")) != "" {
		t.Error("branch present after RemoveSandboxBranches")
	}
}

// TestSyncSrcsWarnsEmptyInclude: include patterns that match nothing yield a
// loud notice instead of a silently empty sandbox.
func TestSyncSrcsWarnsEmptyInclude(t *testing.T) {
	repo := gitRepoWithCommit(t)
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WriteProfileJSON("typo", []byte(`{"srcs":[{"src":".","include":["/no-such-dir/"]}]}`)); err != nil {
		t.Fatal(err)
	}
	var progress strings.Builder
	if err := b.MakeSandbox("typo", &progress); err != nil {
		t.Fatalf("MakeSandbox: %v", err)
	}
	if !strings.Contains(progress.String(), "include matched no files") {
		t.Errorf("expected an empty-selection notice, got %q", progress.String())
	}
}

// TestSyncSrcsRejectsPreSrcsLayout: a sandbox whose dir is itself a worktree
// (the pre-srcs layout) is refused with a recreate hint instead of nesting new
// worktrees inside the old one.
func TestSyncSrcsRejectsPreSrcsLayout(t *testing.T) {
	repo := gitRepoWithCommit(t)
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := worktree.Ensure(repo, b.SandboxDir("old"), "feat/old-sb", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SyncSrcs("old", io.Discard); err == nil || !strings.Contains(err.Error(), "recreate") {
		t.Errorf("SyncSrcs on pre-srcs layout = %v, want a recreate hint", err)
	}
}
