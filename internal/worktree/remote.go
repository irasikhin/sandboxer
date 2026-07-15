package worktree

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// remoteSchemes are the git transport prefixes that mark a src as a URL to be
// cloned rather than a local path to be worktree'd in place. file:// is included
// deliberately: it means "clone a copy of this local repo" (a separate cache),
// as distinct from a bare path which means "worktree the live repo".
var remoteSchemes = []string{
	"ssh://", "git://", "http://", "https://", "ftp://", "ftps://", "file://",
}

// IsRemoteURL reports whether src is a git URL (to be cloned into a host-side
// cache) rather than a local filesystem path (worktree'd in place). It mirrors
// git's own URL detection: an explicit transport scheme, or the scp-like
// short form user@host:path (a colon before the first slash, no scheme). A
// leading ., / or ~ is always a local path.
func IsRemoteURL(src string) bool {
	for _, s := range remoteSchemes {
		if strings.HasPrefix(src, s) {
			return true
		}
	}
	if strings.HasPrefix(src, ".") || strings.HasPrefix(src, "/") || strings.HasPrefix(src, "~") {
		return false
	}
	// scp-like: [user@]host:path — the part before the first colon carries no
	// slash (else it is a path like "sub/dir:tag" and stays local).
	if i := strings.IndexByte(src, ':'); i > 0 && !strings.Contains(src[:i], "/") {
		return true
	}
	return false
}

// RepoName derives a filesystem-friendly repository name from a git URL — the
// last path segment with any trailing ".git" and slashes stripped. Falls back
// to "repo" when the URL has no usable segment.
func RepoName(url string) string {
	u := strings.TrimRight(url, "/")
	u = strings.TrimSuffix(u, ".git")
	// scp-like git@host:org/repo → take after the last ':' or '/'.
	if i := strings.LastIndexAny(u, ":/"); i >= 0 {
		u = u[i+1:]
	}
	u = strings.TrimSuffix(u, ".git")
	if u == "" {
		return "repo"
	}
	return u
}

// Clone clones url into dest as an ordinary (non-bare) checkout, then DETACHES
// HEAD so no branch is checked out in the cache — freeing every branch
// (including the default) for `git worktree add`, exactly as a bare repo would,
// while keeping Detect/Ensure working unchanged (they need a work tree + HEAD).
// The clone uses the host's git configuration and credentials; nothing about it
// enters the container. dest must not already exist.
func Clone(url, dest string, w io.Writer) error {
	if w != nil {
		fmt.Fprintf(w, "cloning %s …\n", url)
	}
	parent := filepath.Dir(dest)
	if _, err := run(parent, "clone", url, filepath.Base(dest)); err != nil {
		return fmt.Errorf("git clone %s: %w", url, err)
	}
	if _, err := run(dest, "checkout", "--detach"); err != nil {
		return fmt.Errorf("git checkout --detach %s: %w", dest, err)
	}
	return nil
}

// FetchCache refreshes a cached clone of a REMOTE src from its origin: it
// updates the remote-tracking refs (never force-updating a local branch, so
// agent commits are never lost) and re-points the detached HEAD at origin's
// default tip, so a branch minted later (recreate --full) forks off the latest
// upstream. Named apart from Fetch, which refreshes the remotes of a LOCAL
// source repo before a reset — different subject, different signature.
// Best-effort on the re-point; a fetch failure (offline, auth) is returned.
func FetchCache(cacheRepo string, w io.Writer) error {
	if w != nil {
		fmt.Fprintf(w, "fetching %s …\n", filepath.Base(cacheRepo))
	}
	if _, err := run(cacheRepo, "fetch", "--prune", "origin"); err != nil {
		return fmt.Errorf("git fetch: %w", err)
	}
	if ref, err := run(cacheRepo, "rev-parse", "--verify", "-q", "origin/HEAD"); err == nil {
		_, _ = run(cacheRepo, "checkout", "--detach", strings.TrimSpace(ref))
	}
	return nil
}

// PrepareBranch makes sure a local branch named branch exists in a cached clone
// before it is worktree'd: if the branch is not already a local head but does
// exist on origin, it is created tracking origin/<branch>, so `srcs branch: X`
// naming a REMOTE branch checks that branch out rather than forking a new one
// off the default. A branch absent both locally and on origin is left to Ensure,
// which creates it off HEAD (a genuinely new branch). Best-effort.
func PrepareBranch(cacheRepo, branch string) {
	if _, err := run(cacheRepo, "rev-parse", "--verify", "-q", "refs/heads/"+branch); err == nil {
		return // already a local head — keep it (may carry the agent's commits)
	}
	if _, err := run(cacheRepo, "rev-parse", "--verify", "-q", "refs/remotes/origin/"+branch); err != nil {
		return // not on origin either — Ensure will create it off HEAD
	}
	_, _ = run(cacheRepo, "branch", branch, "refs/remotes/origin/"+branch)
}
