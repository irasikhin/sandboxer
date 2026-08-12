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
	return gitRepoAt(t, t.TempDir())
}

// gitRepoAt builds the same repo at a fixed path, for tests that need a
// controlled repo basename (the worktree layout's leaf name).
func gitRepoAt(t *testing.T, repo string) string {
	t.Helper()
	requireGit(t)
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
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
	// then: the worktree lives UNDER <slug>/, grouped by branch with the repo
	// as the leaf (…-sandboxes/wt/feat/wt/<repo>), every file present and clean.
	want := filepath.Join(b.SandboxDir("wt"), "feat", "wt", filepath.Base(repo))
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
	// and: an unnarrowed sandbox mounts its <slug>/ root and nothing else
	if mountDest, m, err := Mounts(srcs); err != nil || !mountDest || len(m) != 0 {
		t.Errorf("Mounts = (%v, %v, %v), want (true, none, nil) — managed lives under <slug>/", mountDest, m, err)
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

// TestMakeSandboxIncludeKeepsHostTreeWhole is the whole point of view mounts:
// `include` narrows what the CONTAINER sees, and nothing else. The host keeps a
// complete checkout — that is what lets an IDE open the branch, which a sparse
// worktree made impossible.
func TestMakeSandboxIncludeKeepsHostTreeWhole(t *testing.T) {
	repo := gitRepoWithCommit(t)
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}

	// given: a stored profile narrowing the container's view to serviceA
	if err := b.WriteProfileJSON("narrow", []byte(`{"srcs":[{"src":".","branch":"feat/narrow","include":["/serviceA/"]}]}`)); err != nil {
		t.Fatal(err)
	}

	if err := b.MakeSandbox("narrow", io.Discard); err != nil {
		t.Fatalf("MakeSandbox: %v", err)
	}
	s := b.Srcs("narrow")[0]

	// then: the HOST worktree is complete — the excluded files are right there,
	// which is precisely why the container must not get a mount of the root.
	for _, p := range []string{"serviceA", "serviceB", "CLAUDE.md"} {
		if _, err := os.Stat(filepath.Join(s.Path, p)); err != nil {
			t.Errorf("host worktree is not whole — %s missing: %v", p, err)
		}
	}
	// git exits non-zero with "not sparse" on a full checkout — that IS the
	// assertion, so this call must tolerate the failure rather than use runGit.
	if out, err := exec.Command("git", "-C", s.Path, "sparse-checkout", "list").CombinedOutput(); err == nil {
		t.Errorf("worktree is sparse, want a full checkout:\n%s", out)
	}
	if st := runGit(t, s.Path, "status", "--porcelain"); strings.TrimSpace(st) != "" {
		t.Errorf("worktree not clean:\n%s", st)
	}

	// and: the container gets ONLY serviceA, mounted directly — the root, which
	// holds serviceB and CLAUDE.md, is not mounted at all.
	mountDest, mounts, err := Mounts(b.Srcs("narrow"))
	if err != nil {
		t.Fatal(err)
	}
	if mountDest {
		t.Fatal("narrowed sandbox mounts its root — serviceB and CLAUDE.md would be exposed")
	}
	if len(mounts) != 1 || mounts[0] != filepath.Join(s.Path, "serviceA") {
		t.Errorf("mounts = %v, want [%q]", mounts, filepath.Join(s.Path, "serviceA"))
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
	if srcs[1].Path != filepath.Join(b.SandboxDir("live"), "feat", "live-other", filepath.Base(other)) {
		t.Errorf("second source %q not at its branch/repo path", srcs[1].Path)
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

// TestSrcsAdoptExistingWorktree: a srcs entry whose branch is already checked
// out in a LINKED worktree the user made adopts that checkout — git allows only
// one worktree per branch. It surfaces as an unmanaged mount AND as a symlink
// at its slot inside the sandbox dir, so the container's workdir reaches it like
// any other source; its include narrows the mount set exactly as a managed
// source's does, and the checkout on disk stays complete.
func TestSrcsAdoptExistingWorktree(t *testing.T) {
	repo := gitRepoWithCommit(t)
	mine := filepath.Join(t.TempDir(), "mine")
	runGit(t, repo, "worktree", "add", "-q", "-b", "feat/mine", mine)
	project := t.TempDir()
	b, err := ResolveBase(project)
	if err != nil {
		t.Fatal(err)
	}
	pj := fmt.Sprintf(`{"srcs":[{"src":%q,"branch":"feat/mine","include":["/serviceA/"]}]}`, repo)
	if err := b.WriteProfileJSON("adopt", []byte(pj)); err != nil {
		t.Fatal(err)
	}

	var progress strings.Builder
	if err := b.MakeSandbox("adopt", &progress); err != nil {
		t.Fatalf("MakeSandbox: %v", err)
	}
	srcs := b.Srcs("adopt")
	if len(srcs) != 1 || srcs[0].Managed || srcs[0].Path != mine {
		t.Fatalf("adopted source wrong: %+v", srcs)
	}
	// The slot inside the sandbox dir is a symlink to the adopted checkout:
	// without it the source is mounted but unreachable from the workdir.
	link := filepath.Join(b.SandboxDir("adopt"), "feat", "mine", filepath.Base(repo))
	if srcs[0].Link != link {
		t.Errorf("Link = %q, want %q", srcs[0].Link, link)
	}
	if got, err := os.Readlink(link); err != nil || got != mine {
		t.Errorf("readlink %s = (%q, %v), want %q", link, got, err, mine)
	}
	// include is HONORED on an adopted source (narrowing is a mount-set
	// concern), and the checkout itself is never narrowed.
	if _, err := os.Stat(filepath.Join(mine, "serviceB", "f.txt")); err != nil {
		t.Errorf("adopted checkout was narrowed: %v", err)
	}
	want := filepath.Join(mine, "serviceA")
	if mountDest, m, err := Mounts(srcs); err != nil || mountDest || len(m) != 1 || m[0] != want {
		t.Errorf("Mounts = (%v, %v, %v), want (false, [%s], nil)", mountDest, m, err, want)
	}
	// teardown never touches an adopted worktree — only the link goes
	b.RemoveState("adopt", false)
	if _, err := os.Stat(filepath.Join(mine, "serviceA", "f.txt")); err != nil {
		t.Errorf("RemoveState touched the adopted worktree: %v", err)
	}
}

// TestAdoptRefusesRepoCheckout: the repository's OWN checkout is never adopted.
// Mounting it would hand the agent the tree the user works in and carry a real
// .git into the container with it — the escape the mount boundary exists to
// prevent. The error names the repo and the branch, so the fix is obvious.
func TestAdoptRefusesRepoCheckout(t *testing.T) {
	repo := gitRepoWithCommit(t)
	runGit(t, repo, "checkout", "-qb", "feat/live")
	project := t.TempDir()
	b, err := ResolveBase(project)
	if err != nil {
		t.Fatal(err)
	}
	pj := fmt.Sprintf(`{"srcs":[{"src":%q,"branch":"feat/live"}]}`, repo)
	if err := b.WriteProfileJSON("live", []byte(pj)); err != nil {
		t.Fatal(err)
	}
	err = b.MakeSandbox("live", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "checked out in the repository itself") {
		t.Fatalf("MakeSandbox = %v, want a refusal to adopt the repo's own checkout", err)
	}
	if !strings.Contains(err.Error(), repo) || !strings.Contains(err.Error(), "feat/live") {
		t.Errorf("refusal should name the checkout and the branch: %v", err)
	}
}

// TestAdoptRefusesOtherSandbox: a branch checked out by ANOTHER sandbox is not
// adopted either — two sandboxes sharing one working tree defeats the point,
// and the second one's own directory would come up empty.
func TestAdoptRefusesOtherSandbox(t *testing.T) {
	repo := gitRepoWithCommit(t)
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	pj := fmt.Sprintf(`{"srcs":[{"src":%q,"branch":"feat/shared"}]}`, repo)
	for _, slug := range []string{"first", "second"} {
		if err := b.WriteProfileJSON(slug, []byte(pj)); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.MakeSandbox("first", io.Discard); err != nil {
		t.Fatalf("MakeSandbox first: %v", err)
	}
	err = b.MakeSandbox("second", io.Discard)
	if err == nil || !strings.Contains(err.Error(), `already checked out by sandbox "first"`) {
		t.Fatalf("MakeSandbox second = %v, want a refusal naming the owning sandbox", err)
	}
}

// TestAdoptedLinkRepairs: the link is CONVERGED on every sync, not created once.
// One left pointing somewhere else is re-pointed; a slot holding something that
// is not a symlink is an error rather than a clobber, because whatever is there
// is real content nobody asked sandboxer to delete.
func TestAdoptedLinkRepairs(t *testing.T) {
	repo := gitRepoWithCommit(t)
	mine := filepath.Join(t.TempDir(), "mine")
	runGit(t, repo, "worktree", "add", "-q", "-b", "feat/mine", mine)
	project := t.TempDir()
	b, err := ResolveBase(project)
	if err != nil {
		t.Fatal(err)
	}
	pj := fmt.Sprintf(`{"srcs":[{"src":%q,"branch":"feat/mine"}]}`, repo)
	if err := b.WriteProfileJSON("adopt", []byte(pj)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("adopt", io.Discard); err != nil {
		t.Fatalf("MakeSandbox: %v", err)
	}
	link := b.Srcs("adopt")[0].Link

	// A link aimed at the wrong tree is corrected, not left to mislead.
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(project, link); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SyncSrcs("adopt", io.Discard); err != nil {
		t.Fatalf("SyncSrcs: %v", err)
	}
	if got, err := os.Readlink(link); err != nil || got != mine {
		t.Errorf("stale link not re-pointed: (%q, %v), want %q", got, err, mine)
	}

	// A real directory in the slot stops the sync and survives it.
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(link, "keep"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = b.SyncSrcs("adopt", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "not a symlink") {
		t.Fatalf("SyncSrcs = %v, want a refusal to clobber the occupied slot", err)
	}
	if _, err := os.Stat(filepath.Join(link, "keep")); err != nil {
		t.Errorf("occupied slot was clobbered: %v", err)
	}
}

// TestAdoptedLinkDropped: when an adopted source leaves srcs, its link goes with
// it — a stale pointer into a tree the container no longer mounts is worse than
// no entry at all — while the checkout it pointed at is untouched.
func TestAdoptedLinkDropped(t *testing.T) {
	repo := gitRepoWithCommit(t)
	mine := filepath.Join(t.TempDir(), "mine")
	runGit(t, repo, "worktree", "add", "-q", "-b", "feat/mine", mine)
	other := gitRepoAt(t, filepath.Join(t.TempDir(), "other"))
	project := t.TempDir()
	b, err := ResolveBase(project)
	if err != nil {
		t.Fatal(err)
	}
	pj := fmt.Sprintf(`{"srcs":[{"src":%q,"branch":"feat/keep"},{"src":%q,"branch":"feat/mine"}]}`, other, repo)
	if err := b.WriteProfileJSON("drop", []byte(pj)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("drop", io.Discard); err != nil {
		t.Fatalf("MakeSandbox: %v", err)
	}
	link := b.Srcs("drop")[1].Link
	if link == "" {
		t.Fatal("adopted source got no link")
	}

	pj = fmt.Sprintf(`{"srcs":[{"src":%q,"branch":"feat/keep"}]}`, other)
	if err := b.WriteProfileJSON("drop", []byte(pj)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SyncSrcs("drop", io.Discard); err != nil {
		t.Fatalf("SyncSrcs: %v", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("stale adopted link %s survived the drop (err=%v)", link, err)
	}
	if _, err := os.Stat(filepath.Join(mine, "serviceA", "f.txt")); err != nil {
		t.Errorf("dropping the source touched the adopted checkout: %v", err)
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
func TestSyncSrcsRejectsIncludeThatIsNotADirectory(t *testing.T) {
	repo := gitRepoWithCommit(t)
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WriteProfileJSON("typo", []byte(`{"srcs":[{"src":".","branch":"feat/typo","include":["/no-such-dir/"]}]}`)); err != nil {
		t.Fatal(err)
	}
	// A typo'd include is a hard error, not a warning: the engine would CREATE
	// the missing mount source as a root-owned directory inside the user's
	// worktree, and the sandbox would come up with an empty view.
	err = b.MakeSandbox("typo", io.Discard)
	if err == nil {
		t.Fatal("MakeSandbox accepted an include naming no directory, want an error")
	}
	for _, want := range []string{"/no-such-dir/", "not a directory", "feat/typo"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	// Nothing was created on the host to satisfy the bad pattern.
	if _, err := os.Stat(filepath.Join(b.SandboxDir("typo"), "feat", "typo", filepath.Base(repo), "no-such-dir")); !os.IsNotExist(err) {
		t.Errorf("the bad include path was materialized on the host (err=%v)", err)
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
// checked out outside the sandbox — in a linked worktree the user made — the
// source ADOPTS that checkout instead of failing git's one-worktree-per-branch
// rule, and never marks the branch deletable (it is not sandboxer's).
func TestBranchAdoptsExistingCheckout(t *testing.T) {
	repo := gitRepoWithCommit(t)
	mine := filepath.Join(t.TempDir(), "mine")
	runGit(t, repo, "worktree", "add", "-q", "-b", "feat/adopted", mine)
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
	if s.Managed || s.AutoBranch || s.Path != mine || s.Branch != "feat/adopted" {
		t.Fatalf("the existing checkout should be adopted: %+v", s)
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
	// then: a fresh worktree at the branch/repo path, on the new branch
	want := filepath.Join(b.SandboxDir("ren"), "devops", "ren2", filepath.Base(repo))
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
	if _, err := os.Stat(filepath.Join(b.SandboxDir("ren"), "feat")); !os.IsNotExist(err) {
		t.Errorf("empty intermediate dir of the old branch survived (err=%v)", err)
	}
}

// TestSyncSrcsWorktreesDirChangeRefused: changing worktreesDir on an existing
// sandbox is refused with a hint to recreate — an in-place relocation would need
// a cross-filesystem worktree move (M6). The old worktree and its uncommitted
// work stay put (no data loss, no half-move).
func TestSyncSrcsWorktreesDirChangeRefused(t *testing.T) {
	repo := gitRepoWithCommit(t)
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WriteProfileJSON("mv", []byte(`{"srcs":[{"src":".","branch":"feat/mv"}]}`)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("mv", io.Discard); err != nil {
		t.Fatal(err)
	}
	oldPath := b.Srcs("mv")[0].Path
	writeFile(t, filepath.Join(oldPath, "wip.txt"), "precious")

	// when: worktreesDir now points elsewhere
	if err := b.WriteProfileJSON("mv", []byte(`{"worktreesDir":"relocated","srcs":[{"src":".","branch":"feat/mv"}]}`)); err != nil {
		t.Fatal(err)
	}
	_, err = b.SyncSrcs("mv", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "recreate") {
		t.Fatalf("worktreesDir change = %v, want a refusal pointing at recreate", err)
	}
	// then: the old worktree's uncommitted work is untouched (no half-move)
	if _, statErr := os.Stat(filepath.Join(oldPath, "wip.txt")); statErr != nil {
		t.Errorf("old worktree work was disturbed by the refused relocation: %v", statErr)
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

// TestSameBranchTwoRepos: two sources on the SAME branch name share one
// branch dir, each repo as its own leaf — <branch>/<repo> each.
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
		srcs[0].Path != filepath.Join(b.SandboxDir("two"), "devops", "x", filepath.Base(repo)) ||
		srcs[1].Path != filepath.Join(b.SandboxDir("two"), "devops", "x", filepath.Base(other)) {
		t.Fatalf("same-branch paths = %+v, want devops/x/<repo> each", srcs)
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

// TestDetachSetsAsideSquattingRepo reproduces the live failure: an agent inside
// the guest ran `git init` over the worktree's pointer file (whose gitdir names
// a host path the guest cannot see), leaving a STANDALONE repo squatting the
// managed path. Dropping that source must set the directory aside — `git
// worktree move` refuses it ("is not a .git file") — and prune the shared
// repo's now-broken admin entry so the branch is checkout-able again.
func TestDetachSetsAsideSquattingRepo(t *testing.T) {
	repo := gitRepoWithCommit(t)
	other := gitRepoWithCommit(t)
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	pj := fmt.Sprintf(`{"srcs":[{"src":".","branch":"feat/sq"},{"src":%q,"branch":"feat/sq2"}]}`, other)
	if err := b.WriteProfileJSON("sq", []byte(pj)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("sq", io.Discard); err != nil {
		t.Fatal(err)
	}
	dropped := b.Srcs("sq")[1]

	// given: the pointer file replaced by a real repo, with work inside
	if err := os.Remove(filepath.Join(dropped.Path, ".git")); err != nil {
		t.Fatal(err)
	}
	runGit(t, dropped.Path, "init", "-q")
	writeFile(t, filepath.Join(dropped.Path, "wip.txt"), "precious")

	// when: that source is dropped from srcs
	if err := b.WriteProfileJSON("sq", []byte(`{"srcs":[{"src":".","branch":"feat/sq"}]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SyncSrcs("sq", io.Discard); err != nil {
		t.Fatalf("SyncSrcs over a squatting repo: %v", err)
	}
	// then: moved aside with its contents, never driven through git worktree
	if _, err := os.Stat(dropped.Path); !os.IsNotExist(err) {
		t.Errorf("squatting repo still under the sandbox dir (err=%v)", err)
	}
	moved, err := filepath.Glob(filepath.Join(b.detachedDir("sq"), "sq-*", "wip.txt"))
	if err != nil || len(moved) != 1 {
		t.Errorf("squatting repo's work not preserved under _detached: %v (err=%v)", moved, err)
	}
	// and: the shared repo no longer holds the stale admin entry, so the
	// branch is free for a fresh checkout.
	if out := runGit(t, other, "worktree", "list"); strings.Contains(out, dropped.Path) {
		t.Errorf("stale worktree admin entry survived the set-aside:\n%s", out)
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
	if want := filepath.Join(custom, "wd", "feat", "wd", filepath.Base(repo)); s.Path != want {
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
	if got, want := b.Srcs("wd2")[0].Path, filepath.Join(repo, "sb", "wd2", "feat", "wd2", filepath.Base(repo)); got != want {
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
	// (dirtied first — a clean worktree would just be removed)
	writeFile(t, filepath.Join(b.Srcs("cd")[0].Path, "wip.txt"), "precious")
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
	if err := worktree.Ensure(repo, b.SandboxDir("old"), "feat/old", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SyncSrcs("old", io.Discard); err == nil || !strings.Contains(err.Error(), "recreate") {
		t.Errorf("SyncSrcs on pre-srcs layout = %v, want a recreate hint", err)
	}
}

// TestSyncSrcsDropCleanRemoves: dropping a source whose worktree is CLEAN
// removes the worktree outright — its commits live on the branch, which is
// kept — so _detached/ only ever collects real uncommitted work.
func TestSyncSrcsDropCleanRemoves(t *testing.T) {
	repo := gitRepoWithCommit(t)
	other := gitRepoWithCommit(t)
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	pj := fmt.Sprintf(`{"srcs":[{"src":".","branch":"feat/dc"},{"src":%q,"branch":"feat/dc2"}]}`, other)
	if err := b.WriteProfileJSON("dc", []byte(pj)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("dc", io.Discard); err != nil {
		t.Fatal(err)
	}
	dropped := b.Srcs("dc")[1]

	// when: the clean second source is dropped from srcs
	if err := b.WriteProfileJSON("dc", []byte(`{"srcs":[{"src":".","branch":"feat/dc"}]}`)); err != nil {
		t.Fatal(err)
	}
	var progress strings.Builder
	if _, err := b.SyncSrcs("dc", &progress); err != nil {
		t.Fatal(err)
	}
	// then: the worktree is gone — from <slug>/ AND from _detached/ — with a
	// notice, its admin entry pruned, and the branch kept.
	if _, err := os.Stat(dropped.Path); !os.IsNotExist(err) {
		t.Errorf("dropped clean worktree still present (err=%v)", err)
	}
	if _, err := os.Stat(b.detachedDir("dc")); !os.IsNotExist(err) {
		t.Errorf("clean worktree was set aside, want removed (err=%v)", err)
	}
	if !strings.Contains(progress.String(), "clean worktree removed") {
		t.Errorf("expected a clean-removal notice, got %q", progress.String())
	}
	if strings.Contains(runGit(t, other, "worktree", "list"), dropped.Path) {
		t.Error("dropped worktree's admin entry survived, want pruned")
	}
	if !strings.Contains(runGit(t, other, "branch", "--list", "feat/dc2"), "feat/dc2") {
		t.Error("branch feat/dc2 deleted by the drop, want kept")
	}
}

// TestSyncSrcsReattachSetAside: naming a branch whose worktree sits under
// _detached/ moves it BACK to the managed path (uncommitted work intact)
// instead of adopting the set-aside location — a sandbox must never depend on
// a directory that `clean --detached` destroys.
func TestSyncSrcsReattachSetAside(t *testing.T) {
	repo := gitRepoWithCommit(t)
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WriteProfileJSON("ra", []byte(`{"srcs":[{"src":".","branch":"feat/ra","include":["/serviceA/"]}]}`)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("ra", io.Discard); err != nil {
		t.Fatal(err)
	}
	first := b.Srcs("ra")[0]
	writeFile(t, filepath.Join(first.Path, "wip.txt"), "precious")

	// given: a branch change set the dirty worktree aside under _detached/
	if err := b.WriteProfileJSON("ra", []byte(`{"srcs":[{"src":".","branch":"feat/ra2"}]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SyncSrcs("ra", io.Discard); err != nil {
		t.Fatal(err)
	}
	if moved, err := filepath.Glob(filepath.Join(b.detachedDir("ra"), "ra-*", "wip.txt")); err != nil || len(moved) != 1 {
		t.Fatalf("precondition: work not set aside under _detached: %v (err=%v)", moved, err)
	}

	// when: the config names feat/ra again
	if err := b.WriteProfileJSON("ra", []byte(`{"srcs":[{"src":".","branch":"feat/ra","include":["/serviceA/"]}]}`)); err != nil {
		t.Fatal(err)
	}
	var progress strings.Builder
	srcs, err := b.SyncSrcs("ra", &progress)
	if err != nil {
		t.Fatal(err)
	}
	// then: the worktree is back at its managed path, work intact, include
	// re-applied, nothing adopted and nothing left under _detached/.
	want := filepath.Join(b.SandboxDir("ra"), "feat", "ra", filepath.Base(repo))
	if len(srcs) != 1 || !srcs[0].Managed || srcs[0].Path != want {
		t.Fatalf("re-attached source = %+v, want managed at %q", srcs, want)
	}
	if _, err := os.Stat(filepath.Join(want, "wip.txt")); err != nil {
		t.Errorf("uncommitted work did not return with the worktree: %v", err)
	}
	if !strings.Contains(progress.String(), "re-attached") {
		t.Errorf("expected a re-attach notice, got %q", progress.String())
	}
	if _, err := os.Stat(b.detachedDir("ra")); !os.IsNotExist(err) {
		t.Errorf("_detached survived the re-attach (err=%v)", err)
	}
	// The source is narrowed, so it is mounted by view — never via the root.
	mountDest, m, err := Mounts(srcs)
	if err != nil {
		t.Fatal(err)
	}
	if mountDest {
		t.Error("Mounts mounts the root for a narrowed source — the whole repo would be exposed")
	}
	if len(m) != 1 || m[0] != filepath.Join(want, "serviceA") {
		t.Errorf("Mounts view = %v, want [%q]", m, filepath.Join(want, "serviceA"))
	}
}

// TestSyncSrcsRecoversFromHandDeletedRoot: `rm -rf ./sandboxes` by hand must
// never wedge the project — the next sync prunes the stale worktree
// registrations and checks the branches out fresh (commits live on the
// branches; deleting the tree by hand forfeits only uncommitted work).
func TestSyncSrcsRecoversFromHandDeletedRoot(t *testing.T) {
	repo := gitRepoWithCommit(t)
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WriteProfileJSON("hd", []byte(`{"srcs":[{"src":".","branch":"feat/hd"}]}`)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("hd", io.Discard); err != nil {
		t.Fatal(err)
	}

	// when: the whole worktrees root is deleted by hand, then the sandbox is
	// made again (what enter does when the dir is gone)
	if err := os.RemoveAll(SandboxesRoot(repo)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("hd", io.Discard); err != nil {
		t.Fatalf("MakeSandbox after rm -rf sandboxes: %v", err)
	}
	// then: a fresh managed worktree on the same branch, no stale registration
	s := b.Srcs("hd")[0]
	if !s.Managed || s.Branch != "feat/hd" || !worktree.IsWorktree(s.Path) {
		t.Fatalf("recovered source wrong: %+v", s)
	}
	if br := strings.TrimSpace(runGit(t, s.Path, "rev-parse", "--abbrev-ref", "HEAD")); br != "feat/hd" {
		t.Errorf("recovered worktree on %q, want feat/hd", br)
	}
}

// TestGenBumpsOnDirCreation: the sandbox-dir generation advances exactly when
// the dir is created from nothing — first sync, or after a hand `rm -rf` —
// and never on an ordinary re-sync.
func TestGenBumpsOnDirCreation(t *testing.T) {
	repo := gitRepoWithCommit(t)
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	if g := b.Gen("g"); g != "" {
		t.Fatalf("Gen before any sync = %q, want empty", g)
	}
	if err := b.WriteProfileJSON("g", []byte(`{"srcs":[{"src":".","branch":"feat/g"}]}`)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("g", io.Discard); err != nil {
		t.Fatal(err)
	}
	if g := b.Gen("g"); g != "1" {
		t.Fatalf("Gen after first sync = %q, want 1", g)
	}
	if _, err := b.SyncSrcs("g", io.Discard); err != nil {
		t.Fatal(err)
	}
	if g := b.Gen("g"); g != "1" {
		t.Errorf("Gen after a plain re-sync = %q, want still 1", g)
	}
	if err := os.RemoveAll(SandboxesRoot(repo)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("g", io.Discard); err != nil {
		t.Fatal(err)
	}
	if g := b.Gen("g"); g != "2" {
		t.Errorf("Gen after rm -rf + resync = %q, want 2", g)
	}
	// rm wipes the counter with the rest of the sandbox state
	b.RemoveState("g", false)
	if g := b.Gen("g"); g != "" {
		t.Errorf("Gen after RemoveState = %q, want empty", g)
	}
}

// TestSrcsStaleForeignRegistration: a branch git still registers to a checkout
// that was deleted by hand SOMEWHERE ELSE (another project's sandboxes, an old
// worktree) is not adopted — the stale registration is pruned and a managed
// worktree checked out fresh.
func TestSrcsStaleForeignRegistration(t *testing.T) {
	repo := gitRepoWithCommit(t)
	elsewhere := filepath.Join(t.TempDir(), "gone")
	runGit(t, repo, "worktree", "add", "-b", "feat/fr", elsewhere)
	if err := os.RemoveAll(elsewhere); err != nil {
		t.Fatal(err)
	}

	project := t.TempDir()
	b, err := ResolveBase(project)
	if err != nil {
		t.Fatal(err)
	}
	pj := fmt.Sprintf(`{"srcs":[{"src":%q,"branch":"feat/fr"}]}`, repo)
	if err := b.WriteProfileJSON("fr", []byte(pj)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("fr", io.Discard); err != nil {
		t.Fatalf("MakeSandbox with a stale foreign registration: %v", err)
	}
	s := b.Srcs("fr")[0]
	if !s.Managed || !strings.HasPrefix(s.Path, b.SandboxDir("fr")+string(filepath.Separator)) {
		t.Fatalf("source = %+v, want managed under the sandbox dir (never the stale path)", s)
	}
	if br := strings.TrimSpace(runGit(t, s.Path, "rev-parse", "--abbrev-ref", "HEAD")); br != "feat/fr" {
		t.Errorf("worktree on %q, want feat/fr", br)
	}
}

// TestMakeSandboxRejectsSymlinkEscape drives the full create flow: a repo with a
// checked-in symlink pointing OUTSIDE the worktree, narrowed to that symlink,
// must be refused before anything is mounted — otherwise the engine would
// bind-mount the host target (e.g. /etc) into the container, past the wall.
func TestMakeSandboxRejectsSymlinkEscape(t *testing.T) {
	repo := gitRepoWithCommit(t)
	// A secret OUTSIDE the repo, and a checked-in symlink to its dir.
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.txt"), "HOST-SECRET")
	if err := os.Symlink(outside, filepath.Join(repo, "escape")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-qm", "add escape symlink")

	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WriteProfileJSON("esc", []byte(`{"srcs":[{"src":".","branch":"feat/esc","include":["/escape/"]}]}`)); err != nil {
		t.Fatal(err)
	}
	err = b.MakeSandbox("esc", io.Discard)
	if err == nil {
		t.Fatal("MakeSandbox accepted a symlink-escape include — host files would be exposed")
	}
	if !strings.Contains(err.Error(), "outside the worktree") {
		t.Errorf("error = %q, want it to name the containment escape", err)
	}
}
