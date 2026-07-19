package worktree

import (
	"fmt"
	"strconv"
	"strings"
)

// Fetch updates the source repo's remote-tracking refs from origin (pruning
// deleted ones), so a following reset onto origin/<branch> lands on the
// freshly-merged state. dir may be the worktree: a linked worktree shares the
// repo's remotes and object store.
func Fetch(dir string) error {
	if _, err := run(dir, "fetch", "origin", "--prune"); err != nil {
		return fmt.Errorf("git fetch origin: %w", err)
	}
	return nil
}

// IsClean reports whether the worktree at dir has no uncommitted changes
// (nothing staged, unstaged, or untracked) — the guard before a hard reset
// throws local work away.
func IsClean(dir string) (bool, error) {
	out, err := run(dir, "status", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("git status: %w", err)
	}
	return strings.TrimSpace(out) == "", nil
}

// ResetHard moves the worktree's current branch onto ref (HEAD, index and
// working tree), staying ON the branch — it never checks ref out, so a base
// branch already checked out in another worktree is never contended.
func ResetHard(dir, ref string) error {
	if _, err := run(dir, "reset", "--hard", ref); err != nil {
		return fmt.Errorf("git reset --hard %s: %w", ref, err)
	}
	return nil
}

// ShortHash returns the abbreviated commit hash ref resolves to in dir (for
// reporting where a reset landed, and as a pre-flight that a base ref exists).
// Best-effort: "" when ref cannot be resolved.
func ShortHash(dir, ref string) string {
	out, err := run(dir, "rev-parse", "--short", ref)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// Ahead reports how many commits the worktree's HEAD has that ref (the reset
// base) does not — the commits a `reset --hard ref` would abandon (they survive
// only in the reflog). It is the commit-level counterpart of IsClean, which sees
// only the working tree: running reset before the branch's PR has actually
// merged would otherwise silently discard the un-merged commits. An error is
// returned (not a zero) so the caller treats "cannot tell" as "not proven safe".
func Ahead(dir, ref string) (int, error) {
	out, err := run(dir, "rev-list", "--count", ref+"..HEAD")
	if err != nil {
		return 0, fmt.Errorf("git rev-list %s..HEAD: %w", ref, err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("parse commit count %q: %w", strings.TrimSpace(out), err)
	}
	return n, nil
}
