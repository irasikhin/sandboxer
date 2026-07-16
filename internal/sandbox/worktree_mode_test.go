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

	// when: a sandbox is made from the scaffold-style explicit {src, branch}
	if err := b.WriteProfileJSON("wt", []byte(`{"srcs":[{"src":".","branch":"feat/wt"}]}`)); err != nil {
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
	if !s.Managed || !s.AutoBranch || s.Branch != "feat/wt" || s.RepoRoot != repo {
		t.Errorf("dot source wrong: %+v", s)
	}
	// then: the worktree lives UNDER <slug>/, grouped by repo and named by
	// branch (…-sandboxes/wt/<repo>/feat/wt), every file present and clean.
	want := filepath.Join(b.SandboxDir("wt"), filepath.Base(repo), "feat", "wt")
	if s.Path != want {
		t.Fatalf("worktree path %q, want %q", s.Path, want)
	}
	if worktree.IsWorktree(b.SandboxDir("wt")) {
		t.Error("sandbox dir itself must not be a worktree")
	}
	for _, p := range []string{"serviceA", "serviceB", "CLAUDE.md", ".git"} {
		if _, err := os.Stat(filepath.Join(s.Path, p)); err != nil {
			t.Errorf("missing %s in worktree: %v", p, err)
		}
	}
	if br := strings.TrimSpace(runGit(t, s.Path, "rev-parse", "--abbrev-ref", "HEAD")); br != "feat/wt" {
		t.Errorf("branch = %q, want feat/wt", br)
	}
	if st := runGit(t, s.Path, "status", "--porcelain"); strings.TrimSpace(st) != "" {
		t.Errorf("worktree not clean:\n%s", st)
	}
	// and: nothing extra is mounted for a fully managed sandbox
	if m := SrcMounts(srcs); len(m) != 0 {
		t.Errorf("SrcMounts = %v, want none (managed lives under <slug>/)", m)
	}
	// and: the in-project ./sandboxes root was git-ignored, exactly once even
	// after a re-sync.
	if _, err := b.SyncSrcs("wt", io.Discard); err != nil {
		t.Fatal(err)
	}
	gi, err := os.ReadFile(filepath.Join(repo, ".gitignore"))
	if err != nil {
		t.Fatalf("no .gitignore written for the in-project root: %v", err)
	}
	if got := strings.Count(string(gi), "/sandboxes/"); got != 1 {
		t.Errorf(".gitignore has %d /sandboxes/ entries, want exactly 1:\n%s", got, gi)
	}
	if st := runGit(t, repo, "status", "--porcelain", "--ignored=no", "--", "sandboxes"); strings.TrimSpace(st) != "" {
		t.Errorf("sandboxes/ visible in the project's git status:\n%s", st)
	}
}

func TestMakeSandboxSparseInclude(t *testing.T) {
	repo := gitRepoWithCommit(t)
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}

	// given: a stored profile narrowing to serviceA via a gitignore pattern
	if err := b.WriteProfileJSON("narrow", []byte(`{"srcs":[{"src":".","branch":"feat/narrow","include":["/serviceA/"]}]}`)); err != nil {
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
	if err := b.WriteProfileJSON("live", []byte(`{"srcs":[{"src":".","branch":"feat/live","include":["/serviceA/"]}]}`)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("live", io.Discard); err != nil {
		t.Fatal(err)
	}
	first := b.Srcs("live")[0]

	// when: the profile widens the include and adds a second repo
	pj := fmt.Sprintf(`{"srcs":[{"src":".","branch":"feat/live"},{"src":%q,"branch":"feat/live-other"}]}`, other)
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
	if srcs[1].Path != filepath.Join(b.SandboxDir("live"), filepath.Base(other), "feat", "live-other") {
		t.Errorf("second source %q not at its repo/branch path", srcs[1].Path)
	}

	// when: the second repo is dropped again, with uncommitted work inside
	writeFile(t, filepath.Join(srcs[1].Path, "wip.txt"), "precious")
	if err := b.WriteProfileJSON("live", []byte(`{"srcs":[{"src":".","branch":"feat/live"}]}`)); err != nil {
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
	moved, err := filepath.Glob(filepath.Join(b.detachedDir("live"), "live-*", "wip.txt"))
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
		{"non-git", fmt.Sprintf(`{"srcs":[{"src":%q,"branch":"feat/bad"}]}`, nonGit), "not a git repository"},
		{"missing", `{"srcs":[{"src":"./no-such-dir","branch":"feat/bad"}]}`, "no such directory"},
		{"subdir", `{"srcs":[{"src":"./serviceA","branch":"feat/bad"}]}`, "repository root"},
		{"dup", fmt.Sprintf(`{"srcs":[{"src":".","branch":"feat/bad"},{"src":%q,"branch":"feat/bad2"}]}`, repo), "listed twice"},
		{"empty", `{"srcs":[{"src":""}]}`, "empty src"},
		{"no-srcs", `{}`, "srcs is empty"},
		{"no-branch", `{"srcs":[{"src":"."}]}`, "branch is required"},
		{"bad-branch", `{"srcs":[{"src":".","branch":"-bad..name"}]}`, "invalid branch name"},
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
	if err := b.WriteProfileJSON("gone", []byte(`{"srcs":[{"src":".","branch":"feat/gone"}]}`)); err != nil {
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
	if !strings.Contains(runGit(t, repo, "branch", "--list", "feat/gone"), "feat/gone") {
		t.Error("branch removed by RemoveState, want kept")
	}

	// and: RemoveSandboxBranches then deletes the auto-named one (recreate --full)
	b.RemoveSandboxBranches("gone", srcs)
	if strings.TrimSpace(runGit(t, repo, "branch", "--list", "feat/gone")) != "" {
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
	if err := b.WriteProfileJSON("typo", []byte(`{"srcs":[{"src":".","branch":"feat/typo","include":["/no-such-dir/"]}]}`)); err != nil {
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

// TestReusePreexistingBranch: a configured branch that already exists is
// REUSED (checked out into the managed worktree), and because sandboxer did
// not mint it, a full reset keeps it.
func TestReusePreexistingBranch(t *testing.T) {
	repo := gitRepoWithCommit(t)
	runGit(t, repo, "branch", "feat/mine")
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WriteProfileJSON("mine", []byte(`{"srcs":[{"src":".","branch":"feat/mine"}]}`)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("mine", io.Discard); err != nil {
		t.Fatal(err)
	}
	srcs := b.Srcs("mine")
	if len(srcs) != 1 || srcs[0].Branch != "feat/mine" || !srcs[0].Managed || srcs[0].AutoBranch {
		t.Fatalf("pre-existing branch should be reused, not owned: %+v", srcs)
	}
	// A re-sync must not flip the verdict (the branch exists now either way).
	if _, err := b.SyncSrcs("mine", io.Discard); err != nil {
		t.Fatal(err)
	}
	if s := b.Srcs("mine")[0]; s.AutoBranch {
		t.Errorf("re-sync minted ownership of a user branch: %+v", s)
	}
	b.RemoveState("mine", true)
	b.RemoveSandboxBranches("mine", srcs)
	if !strings.Contains(runGit(t, repo, "branch", "--list", "feat/mine"), "feat/mine") {
		t.Error("full reset deleted a branch sandboxer did not create")
	}
}

// TestBranchAdoptsExistingCheckout: when the configured branch is already
// checked out outside the sandbox (here: the repo's main checkout), the
// source ADOPTS that checkout instead of failing git's one-worktree-per-branch
// rule — and never marks the branch deletable.
func TestBranchAdoptsExistingCheckout(t *testing.T) {
	repo := gitRepoWithCommit(t)
	runGit(t, repo, "checkout", "-qb", "feat/adopted")
	project := t.TempDir()
	b, err := ResolveBase(project)
	if err != nil {
		t.Fatal(err)
	}
	pj := fmt.Sprintf(`{"srcs":[{"src":%q,"branch":"feat/adopted"}]}`, repo)
	if err := b.WriteProfileJSON("adopted", []byte(pj)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("adopted", io.Discard); err != nil {
		t.Fatal(err)
	}
	s := b.Srcs("adopted")[0]
	if s.Managed || s.AutoBranch || s.Path != repo || s.Branch != "feat/adopted" {
		t.Fatalf("default branch should adopt the existing checkout: %+v", s)
	}
}

// TestSyncSrcsBranchChangeSetsAside: changing an entry's branch: is the one
// deliberate way to switch — the old worktree is set aside under _detached/
// (uncommitted work intact) and a fresh one appears at the path named after
// the NEW branch.
func TestSyncSrcsBranchChangeSetsAside(t *testing.T) {
	repo := gitRepoWithCommit(t)
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WriteProfileJSON("ren", []byte(`{"srcs":[{"src":".","branch":"feat/ren"}]}`)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("ren", io.Discard); err != nil {
		t.Fatal(err)
	}
	s := b.Srcs("ren")[0]
	writeFile(t, filepath.Join(s.Path, "wip.txt"), "precious")

	// when: the config names a different branch
	if err := b.WriteProfileJSON("ren", []byte(`{"srcs":[{"src":".","branch":"devops/ren2"}]}`)); err != nil {
		t.Fatal(err)
	}
	srcs, err := b.SyncSrcs("ren", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	// then: a fresh worktree at the repo/branch path, on the new branch
	want := filepath.Join(b.SandboxDir("ren"), filepath.Base(repo), "devops", "ren2")
	if len(srcs) != 1 || srcs[0].Path != want || srcs[0].Branch != "devops/ren2" {
		t.Fatalf("srcs after branch change = %+v, want path %q", srcs, want)
	}
	if br := strings.TrimSpace(runGit(t, want, "rev-parse", "--abbrev-ref", "HEAD")); br != "devops/ren2" {
		t.Errorf("worktree on %q, want devops/ren2", br)
	}
	// and: the old worktree was set aside with its uncommitted work
	moved, err := filepath.Glob(filepath.Join(b.detachedDir("ren"), "ren-*", "wip.txt"))
	if err != nil || len(moved) != 1 {
		t.Errorf("old worktree's work not preserved under _detached: %v (err=%v)", moved, err)
	}
	// and: the old branch's now-empty intermediate dirs were tidied away
	if _, err := os.Stat(filepath.Join(b.SandboxDir("ren"), filepath.Base(repo), "feat")); !os.IsNotExist(err) {
		t.Errorf("empty intermediate dir of the old branch survived (err=%v)", err)
	}
}

// TestMissingBranchHintNamesRecorded: an entry that loses its branch: errors,
// and the error names the branch the sandbox's worktree is recorded on.
func TestMissingBranchHintNamesRecorded(t *testing.T) {
	repo := gitRepoWithCommit(t)
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WriteProfileJSON("hint", []byte(`{"srcs":[{"src":".","branch":"devops/thing"}]}`)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("hint", io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := b.WriteProfileJSON("hint", []byte(`{"srcs":[{"src":"."}]}`)); err != nil {
		t.Fatal(err)
	}
	_, err = b.SyncSrcs("hint", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "branch is required") ||
		!strings.Contains(err.Error(), "devops/thing") {
		t.Errorf("SyncSrcs = %v, want a branch-required error naming devops/thing", err)
	}
}

// TestSameBranchTwoRepos: two sources on the SAME branch name live naturally
// under their own repo dirs — <repo>/<branch> each.
func TestSameBranchTwoRepos(t *testing.T) {
	repo := gitRepoWithCommit(t)
	other := gitRepoWithCommit(t)
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	pj := fmt.Sprintf(`{"srcs":[{"src":".","branch":"devops/x"},{"src":%q,"branch":"devops/x"}]}`, other)
	if err := b.WriteProfileJSON("two", []byte(pj)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("two", io.Discard); err != nil {
		t.Fatal(err)
	}
	srcs := b.Srcs("two")
	if len(srcs) != 2 ||
		srcs[0].Path != filepath.Join(b.SandboxDir("two"), filepath.Base(repo), "devops", "x") ||
		srcs[1].Path != filepath.Join(b.SandboxDir("two"), filepath.Base(other), "devops", "x") {
		t.Fatalf("same-branch paths = %+v, want <repo>/devops/x each", srcs)
	}
	for _, s := range srcs {
		if _, err := os.Stat(filepath.Join(s.Path, "CLAUDE.md")); err != nil {
			t.Errorf("worktree %s not materialized: %v", s.Path, err)
		}
	}
}

// TestSyncSrcsKeepsWorktreeOnDetachedHead: a worktree whose branch cannot be
// read (detached HEAD — a rebase stop, a pinned commit) is left in place with
// a notice, never set aside: "unknown" is not "switched".
func TestSyncSrcsKeepsWorktreeOnDetachedHead(t *testing.T) {
	repo := gitRepoWithCommit(t)
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WriteProfileJSON("det", []byte(`{"srcs":[{"src":".","branch":"feat/det"}]}`)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("det", io.Discard); err != nil {
		t.Fatal(err)
	}
	s := b.Srcs("det")[0]
	runGit(t, s.Path, "checkout", "-q", "--detach")
	writeFile(t, filepath.Join(s.Path, "wip.txt"), "precious")

	var progress strings.Builder
	srcs, err := b.SyncSrcs("det", &progress)
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) != 1 || srcs[0].Path != s.Path {
		t.Fatalf("srcs = %+v, want the same single source", srcs)
	}
	if _, err := os.Stat(filepath.Join(s.Path, "wip.txt")); err != nil {
		t.Errorf("uncommitted work lost: %v", err)
	}
	if _, err := os.Stat(b.detachedDir("det")); !os.IsNotExist(err) {
		t.Errorf("detached-HEAD worktree was set aside (err=%v)", err)
	}
	if !strings.Contains(progress.String(), "not on a branch") {
		t.Errorf("expected a left-in-place notice, got %q", progress.String())
	}
}

// TestDetachSetsAsideNonWorktreeDir: a dropped managed source whose directory
// git no longer recognizes (broken linkage) is moved under _detached/, never
// deleted — the contents may be real work.
func TestDetachSetsAsideNonWorktreeDir(t *testing.T) {
	repo := gitRepoWithCommit(t)
	other := gitRepoWithCommit(t)
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	pj := fmt.Sprintf(`{"srcs":[{"src":".","branch":"feat/brk"},{"src":%q,"branch":"feat/brk2"}]}`, other)
	if err := b.WriteProfileJSON("brk", []byte(pj)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("brk", io.Discard); err != nil {
		t.Fatal(err)
	}
	dropped := b.Srcs("brk")[1]

	// given: the second worktree's git linkage broke, but real work is inside
	if err := os.Remove(filepath.Join(dropped.Path, ".git")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dropped.Path, "wip.txt"), "precious")

	// when: that source is dropped from srcs
	if err := b.WriteProfileJSON("brk", []byte(`{"srcs":[{"src":".","branch":"feat/brk"}]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SyncSrcs("brk", io.Discard); err != nil {
		t.Fatal(err)
	}
	// then: the directory moved aside with its contents, not deleted
	if _, err := os.Stat(dropped.Path); !os.IsNotExist(err) {
		t.Errorf("dropped dir still under the sandbox dir (err=%v)", err)
	}
	moved, err := filepath.Glob(filepath.Join(b.detachedDir("brk"), "brk-*", "wip.txt"))
	if err != nil || len(moved) != 1 {
		t.Errorf("dropped dir's work not preserved under _detached: %v (err=%v)", moved, err)
	}
}

// TestWorktreesDirOverride: a profile worktreesDir relocates the sandbox —
// absolute or relative to the project root — and the project-wide worktree
// sweep honors it without ever removing a user-chosen root wholesale.
func TestWorktreesDirOverride(t *testing.T) {
	repo := gitRepoWithCommit(t)
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	custom := t.TempDir()
	pj := fmt.Sprintf(`{"worktreesDir":%q,"srcs":[{"src":".","branch":"feat/wd"}]}`, custom)
	if err := b.WriteProfileJSON("wd", []byte(pj)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("wd", io.Discard); err != nil {
		t.Fatal(err)
	}
	if got, want := b.SandboxDir("wd"), filepath.Join(custom, "wd"); got != want {
		t.Fatalf("SandboxDir = %q, want %q", got, want)
	}
	s := b.Srcs("wd")[0]
	if want := filepath.Join(custom, "wd", filepath.Base(repo), "feat", "wd"); s.Path != want {
		t.Fatalf("worktree path %q, want %q", s.Path, want)
	}
	if _, err := os.Stat(filepath.Join(s.Path, "CLAUDE.md")); err != nil {
		t.Errorf("worktree not materialized at the custom root: %v", err)
	}
	if _, err := os.Stat(SandboxesRoot(b.Src)); !os.IsNotExist(err) {
		t.Errorf("default root created despite worktreesDir (err=%v)", err)
	}
	// An out-of-project root needs no .gitignore entry.
	if _, err := os.Stat(filepath.Join(repo, ".gitignore")); !os.IsNotExist(err) {
		t.Errorf(".gitignore written for an out-of-project root (err=%v)", err)
	}

	// A relative worktreesDir resolves against the PROJECT ROOT, and an
	// in-project override is git-ignored like the default.
	if err := b.WriteProfileJSON("wd2", []byte(`{"worktreesDir":"./sb","srcs":[{"src":".","branch":"feat/wd2"}]}`)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("wd2", io.Discard); err != nil {
		t.Fatal(err)
	}
	if got, want := b.Srcs("wd2")[0].Path, filepath.Join(repo, "sb", "wd2", filepath.Base(repo), "feat", "wd2"); got != want {
		t.Fatalf("relative worktreesDir path %q, want %q", got, want)
	}
	if gi, err := os.ReadFile(filepath.Join(repo, ".gitignore")); err != nil || !strings.Contains(string(gi), "/sb/") {
		t.Errorf(".gitignore misses the in-project override (/sb/): %q err=%v", gi, err)
	}

	// The project-wide sweep removes the per-sandbox dirs under each custom
	// root — never a user-chosen root wholesale (the project itself survives
	// worktreesDir = "./sb").
	if removed := b.CleanWorktrees(); len(removed) == 0 {
		t.Fatal("CleanWorktrees removed nothing")
	}
	for _, p := range []string{filepath.Join(custom, "wd"), filepath.Join(repo, "sb", "wd2")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("sandbox dir %q survived CleanWorktrees (err=%v)", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, "CLAUDE.md")); err != nil {
		t.Errorf("project files must survive a sweep with worktreesDir inside the repo: %v", err)
	}
}

// TestCleanDetached: the _detached/ sweep removes only the set-aside sources
// — the live sandbox and the branches stay — and prunes the repos' dangling
// worktree admin entries.
func TestCleanDetached(t *testing.T) {
	repo := gitRepoWithCommit(t)
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WriteProfileJSON("cd", []byte(`{"srcs":[{"src":".","branch":"feat/cd"}]}`)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("cd", io.Discard); err != nil {
		t.Fatal(err)
	}
	// given: a branch change set the first worktree aside under _detached/
	if err := b.WriteProfileJSON("cd", []byte(`{"srcs":[{"src":".","branch":"feat/cd2"}]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SyncSrcs("cd", io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(b.detachedDir("cd")); err != nil {
		t.Fatalf("no _detached to clean: %v", err)
	}

	removed, err := b.CleanDetached()
	if err != nil || len(removed) == 0 {
		t.Fatalf("CleanDetached = (%v, %v)", removed, err)
	}
	if _, err := os.Stat(b.detachedDir("cd")); !os.IsNotExist(err) {
		t.Errorf("_detached survived the sweep (err=%v)", err)
	}
	// the live sandbox and both branches are untouched
	live := b.Srcs("cd")[0]
	if _, err := os.Stat(filepath.Join(live.Path, "CLAUDE.md")); err != nil {
		t.Errorf("live sandbox worktree touched by CleanDetached: %v", err)
	}
	for _, br := range []string{"feat/cd", "feat/cd2"} {
		if !strings.Contains(runGit(t, repo, "branch", "--list", br), br) {
			t.Errorf("branch %s deleted by CleanDetached, want kept", br)
		}
	}
	// the dangling admin entry was pruned
	if strings.Contains(runGit(t, repo, "worktree", "list"), "_detached") {
		t.Error("dangling _detached admin entry survived, want pruned")
	}
	// idempotent: a second sweep finds nothing and is not an error
	if removed, err := b.CleanDetached(); err != nil || len(removed) != 0 {
		t.Errorf("second CleanDetached = (%v, %v), want a no-op", removed, err)
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
	if err := worktree.Ensure(repo, b.SandboxDir("old"), "feat/old", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SyncSrcs("old", io.Discard); err == nil || !strings.Contains(err.Error(), "recreate") {
		t.Errorf("SyncSrcs on pre-srcs layout = %v, want a recreate hint", err)
	}
}
