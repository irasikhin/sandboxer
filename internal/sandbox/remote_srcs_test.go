package sandbox

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSyncSrcsRemoteClone: a srcs entry whose src is a git URL is cloned into
// the host-side _remotes cache and worktree'd under <slug>/<repo>/<branch>
// exactly like a local repo — managed, with the clone recorded as Remote.
func TestSyncSrcsRemoteClone(t *testing.T) {
	origin := gitRepoWithCommit(t)
	b, err := ResolveBase(t.TempDir()) // the project root is unrelated to the remote
	if err != nil {
		t.Fatal(err)
	}
	url := "file://" + origin
	pj := fmt.Sprintf(`{"srcs":[{"src":%q,"branch":"feat/rem"}]}`, url)
	if err := b.WriteProfileJSON("rem", []byte(pj)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("rem", io.Discard); err != nil {
		t.Fatalf("MakeSandbox: %v", err)
	}

	srcs := b.Srcs("rem")
	if len(srcs) != 1 {
		t.Fatalf("srcs = %+v, want 1", srcs)
	}
	s := srcs[0]
	if !s.Managed || s.Remote != url {
		t.Errorf("remote source metadata wrong: %+v", s)
	}
	if s.Branch != "feat/rem" {
		t.Errorf("branch = %q, want the named branch", s.Branch)
	}
	// Layout is the same as a local source: <slug>/<repo>/<branch>.
	if want := filepath.Join(b.SandboxDir("rem"), filepath.Base(s.RepoRoot), "feat", "rem"); s.Path != want {
		t.Errorf("worktree path = %q, want %q", s.Path, want)
	}
	if !strings.HasPrefix(s.RepoRoot, b.remotesDir()) {
		t.Errorf("RepoRoot %q not under the _remotes cache", s.RepoRoot)
	}
	if _, err := os.Stat(filepath.Join(s.Path, "CLAUDE.md")); err != nil {
		t.Errorf("remote content not materialized in the worktree: %v", err)
	}
	// The remote is a managed worktree, so it rides the single <slug>/ mount —
	// no extra bind mount (unlike an adopted local worktree).
	mountDest, mounts, err := Mounts(srcs)
	if err != nil {
		t.Fatal(err)
	}
	if !mountDest || len(mounts) != 0 {
		t.Errorf("an unnarrowed remote should ride the single <slug>/ mount, got dest=%v extra=%v", mountDest, mounts)
	}
}

// TestSyncSrcsRemoteBranchInclude: branch: on a remote checks out that remote
// branch, and include narrows the remote exactly as it narrows a local source
// — the MOUNT SET, never the worktree (the host checkout stays complete so an
// IDE can open it; see docs/view-mounts-design.md).
func TestSyncSrcsRemoteBranchInclude(t *testing.T) {
	origin := gitRepoWithCommit(t)
	base := strings.TrimSpace(runGit(t, origin, "rev-parse", "--abbrev-ref", "HEAD"))
	runGit(t, origin, "checkout", "-q", "-b", "feat/proto")
	writeFile(t, filepath.Join(origin, "serviceA", "PROTO.txt"), "proto")
	runGit(t, origin, "add", "-A")
	runGit(t, origin, "commit", "-qm", "proto work")
	runGit(t, origin, "checkout", "-q", base)

	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	url := "file://" + origin
	pj := fmt.Sprintf(`{"srcs":[{"src":%q,"branch":"feat/proto","include":["/serviceA/"]}]}`, url)
	if err := b.WriteProfileJSON("p", []byte(pj)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("p", io.Discard); err != nil {
		t.Fatalf("MakeSandbox: %v", err)
	}
	s := b.Srcs("p")[0]
	if s.Branch != "feat/proto" || s.AutoBranch {
		t.Errorf("expected the named (pre-existing upstream) remote branch, got %+v", s)
	}
	// The branch-specific file is present (branch checked out), narrowed to serviceA.
	if _, err := os.Stat(filepath.Join(s.Path, "serviceA", "PROTO.txt")); err != nil {
		t.Errorf("remote branch content missing: %v", err)
	}
	// The HOST worktree stays complete — include narrows what the CONTAINER
	// sees, so serviceB must still be on disk.
	if _, err := os.Stat(filepath.Join(s.Path, "serviceB", "f.txt")); err != nil {
		t.Errorf("the host worktree must be a complete checkout: %v", err)
	}
	mountDest, mounts, err := Mounts([]Source{s})
	if err != nil {
		t.Fatal(err)
	}
	if mountDest {
		t.Error("a narrowed remote must NOT mount <slug>/ — that absence is the boundary")
	}
	if len(mounts) != 1 || !strings.Contains(mounts[0], "serviceA") {
		t.Errorf("mounts = %v, want only the included serviceA dir", mounts)
	}
}

// TestRefreshRemotesFetches: recreate's refresh point pulls new upstream commits
// into the shared cache, so a subsequently-built sandbox off the same remote
// sees them (clone-once, refresh-on-recreate).
func TestRefreshRemotesFetches(t *testing.T) {
	origin := gitRepoWithCommit(t)
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	url := "file://" + origin
	// A branch is checked out by at most one worktree, so two sandboxes sharing
	// one cached clone name different branches — same rule as a local repo.
	for _, slug := range []string{"one", "two"} {
		pj := fmt.Sprintf(`{"srcs":[{"src":%q,"branch":"feat/%s"}]}`, url, slug)
		if err := b.WriteProfileJSON(slug, []byte(pj)); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.MakeSandbox("one", io.Discard); err != nil {
		t.Fatalf("MakeSandbox one: %v", err)
	}
	if _, err := os.Stat(filepath.Join(b.Srcs("one")[0].Path, "NEW.txt")); !os.IsNotExist(err) {
		t.Fatalf("NEW.txt should not exist before the upstream commit")
	}

	// Upstream advances; refresh the cache (recreate's clone-once refresh point).
	writeFile(t, filepath.Join(origin, "NEW.txt"), "new")
	runGit(t, origin, "add", "-A")
	runGit(t, origin, "commit", "-qm", "upstream advance")
	if err := b.RefreshRemotes("one", io.Discard); err != nil {
		t.Fatalf("RefreshRemotes: %v", err)
	}

	// A fresh sandbox off the same (now-refreshed, shared) cache picks up the commit.
	if err := b.MakeSandbox("two", io.Discard); err != nil {
		t.Fatalf("MakeSandbox two: %v", err)
	}
	if _, err := os.Stat(filepath.Join(b.Srcs("two")[0].Path, "NEW.txt")); err != nil {
		t.Errorf("refreshed cache did not carry the upstream commit to a new sandbox: %v", err)
	}
}

// TestRefreshRemotesSkipsUncloned: RefreshRemotes is recreate's refresh point,
// so it runs against whatever the profile lists — including a remote that was
// never cloned (or a local src, which has no cache at all). Neither is an
// error: resolveSrcs clones on the next sync.
func TestRefreshRemotesSkipsUncloned(t *testing.T) {
	origin := gitRepoWithCommit(t)
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pj := fmt.Sprintf(`{"srcs":[{"src":%q,"branch":"feat/x"},{"src":%q,"branch":"feat/y"}]}`,
		"file://"+origin, origin)
	if err := b.WriteProfileJSON("cold", []byte(pj)); err != nil {
		t.Fatal(err)
	}
	// No MakeSandbox: nothing is cloned yet.
	if err := b.RefreshRemotes("cold", io.Discard); err != nil {
		t.Errorf("RefreshRemotes on an un-cloned profile = %v, want nil", err)
	}
}
