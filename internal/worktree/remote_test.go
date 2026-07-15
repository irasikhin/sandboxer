package worktree

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsRemoteURL(t *testing.T) {
	cases := []struct {
		src  string
		want bool
	}{
		{"https://github.com/foo/bar", true},
		{"https://github.com/foo/bar.git", true},
		{"http://x/y", true},
		{"git://x/y", true},
		{"ssh://git@x/y", true},
		{"ftp://x/y", true},
		{"file:///abs/path", true},
		{"git@github.com:foo/bar.git", true}, // scp-like
		{"user@host:path/to/repo", true},
		{".", false},
		{"./foo", false},
		{"/abs/path", false},
		{"../rel", false},
		{"~/repo", false},
		{"relative/path", false},
		{"sub/dir:tag", false}, // slash before colon → a path, not scp-like
		{"plainname", false},
	}
	for _, c := range cases {
		if got := IsRemoteURL(c.src); got != c.want {
			t.Errorf("IsRemoteURL(%q) = %v, want %v", c.src, got, c.want)
		}
	}
}

func TestRepoName(t *testing.T) {
	cases := map[string]string{
		"https://github.com/foo/bar.git": "bar",
		"https://github.com/foo/bar":     "bar",
		"git@github.com:foo/proto.git":   "proto",
		"ssh://git@host/org/repo":        "repo",
		"file:///abs/path/myrepo":        "myrepo",
		"":                               "repo",
	}
	for url, want := range cases {
		if got := RepoName(url); got != want {
			t.Errorf("RepoName(%q) = %q, want %q", url, got, want)
		}
	}
}

// fileURL turns a local repo path into a git file:// URL git clones as a remote.
func fileURL(path string) string { return "file://" + path }

func defaultBranch(t *testing.T, repo string) string {
	t.Helper()
	return strings.TrimSpace(git(t, repo, "rev-parse", "--abbrev-ref", "HEAD"))
}

// TestCloneDetachedAndWorktree: Clone makes a usable cache (Detect sees it, HEAD
// is detached so every branch is free), and Ensure cuts a worktree off it.
func TestCloneDetachedAndWorktree(t *testing.T) {
	origin := gitRepo(t)
	dest := filepath.Join(t.TempDir(), "cache")

	if err := Clone(fileURL(origin), dest, io.Discard); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	if _, _, ok := Detect(dest); !ok {
		t.Fatal("cloned cache not detected as a repo")
	}
	if b := CurrentBranch(dest); b != "" {
		t.Errorf("cache HEAD should be detached, on %q", b)
	}

	// A managed worktree cuts cleanly off the detached cache, even on the
	// default branch (which a non-detached clone would hold checked out).
	wt := filepath.Join(t.TempDir(), "wt")
	if err := Ensure(dest, wt, defaultBranch(t, origin), io.Discard); err != nil {
		t.Fatalf("Ensure off default branch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, "CLAUDE.md")); err != nil {
		t.Errorf("worktree missing origin content: %v", err)
	}
}

// TestPrepareBranchChecksOutRemoteBranch: a srcs branch: naming a REMOTE branch
// checks that branch out (its content), not a new fork off the default.
func TestPrepareBranchChecksOutRemoteBranch(t *testing.T) {
	origin := gitRepo(t)
	base := defaultBranch(t, origin)
	git(t, origin, "checkout", "-q", "-b", "feat/x")
	if err := os.WriteFile(filepath.Join(origin, "ONLY_ON_X.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, origin, "add", "-A")
	git(t, origin, "commit", "-qm", "x work")
	git(t, origin, "checkout", "-q", base)

	dest := filepath.Join(t.TempDir(), "cache")
	if err := Clone(fileURL(origin), dest, io.Discard); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	PrepareBranch(dest, "feat/x")

	wt := filepath.Join(t.TempDir(), "wt")
	if err := Ensure(dest, wt, "feat/x", io.Discard); err != nil {
		t.Fatalf("Ensure feat/x: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wt, "ONLY_ON_X.txt")); err != nil {
		t.Errorf("branch: feat/x should check out that branch's content: %v", err)
	}
}

// TestFetchAdvances: Fetch pulls new upstream commits into the cache's
// remote-tracking refs and re-points the detached HEAD, without clobbering a
// local branch.
func TestFetchAdvances(t *testing.T) {
	origin := gitRepo(t)
	base := defaultBranch(t, origin)
	dest := filepath.Join(t.TempDir(), "cache")
	if err := Clone(fileURL(origin), dest, io.Discard); err != nil {
		t.Fatalf("Clone: %v", err)
	}

	// A new upstream commit.
	if err := os.WriteFile(filepath.Join(origin, "NEW.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, origin, "add", "-A")
	git(t, origin, "commit", "-qm", "upstream advance")
	want := strings.TrimSpace(git(t, origin, "rev-parse", "HEAD"))

	if err := FetchCache(dest, io.Discard); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	got := strings.TrimSpace(git(t, dest, "rev-parse", "refs/remotes/origin/"+base))
	if got != want {
		t.Errorf("origin/%s after fetch = %s, want %s", base, got, want)
	}
	if head := strings.TrimSpace(git(t, dest, "rev-parse", "HEAD")); head != want {
		t.Errorf("detached HEAD after fetch = %s, want it re-pointed to %s", head, want)
	}
}

// TestPrepareBranchLeavesOtherCasesToEnsure pins the two no-op paths, which are
// what keeps a remote src from clobbering work: a branch that already exists
// locally is NEVER re-pointed at origin (it may carry the agent's commits), and
// a branch on neither side is left for Ensure to mint off HEAD.
func TestPrepareBranchLeavesOtherCasesToEnsure(t *testing.T) {
	origin := gitRepo(t)
	dest := filepath.Join(t.TempDir(), "cache")
	if err := Clone(fileURL(origin), dest, io.Discard); err != nil {
		t.Fatalf("Clone: %v", err)
	}

	// A local branch carrying a commit origin has never seen must survive.
	git(t, dest, "branch", "mine", "HEAD")
	head := strings.TrimSpace(mustRun(t, dest, "rev-parse", "refs/heads/mine"))
	PrepareBranch(dest, "mine")
	if got := strings.TrimSpace(mustRun(t, dest, "rev-parse", "refs/heads/mine")); got != head {
		t.Errorf("an existing local branch was re-pointed: %s -> %s", head, got)
	}

	// A name on neither side stays absent — Ensure creates it off HEAD later.
	PrepareBranch(dest, "nowhere/at/all")
	if _, err := run(dest, "rev-parse", "--verify", "-q", "refs/heads/nowhere/at/all"); err == nil {
		t.Error("PrepareBranch invented a branch that exists on neither side")
	}
}

// mustRun is a git helper that fails the test instead of returning the error.
func mustRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := run(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}
