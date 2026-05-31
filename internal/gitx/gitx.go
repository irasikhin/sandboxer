// Package gitx wraps the git CLI for the operations sandboxer needs: snapshot
// branches in sandbox copies, returning code via cherry-pick, and diffs. It
// shells out to git (matching the original bash) rather than using a library,
// so behavior (cherry-pick, format-patch) is identical.
package gitx

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// identity is injected when the repo has no user.email configured, so snapshot
// commits never fail for lack of an author.
var identity = []string{"-c", "user.email=sandboxer@local", "-c", "user.name=sandboxer"}

// out runs git in dir and returns trimmed stdout, or an error wrapping stderr.
func out(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(se.String())
		if msg == "" {
			msg = err.Error()
		}
		return strings.TrimSpace(so.String()), fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(so.String()), nil
}

// ok runs git and reports only success/failure (stderr discarded).
func ok(dir string, args ...string) bool {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	return cmd.Run() == nil
}

// IsRepo reports whether dir is inside a git working tree.
func IsRepo(dir string) bool {
	return ok(dir, "-C", dir, "rev-parse", "--git-dir")
}

// HeadSHA returns the current HEAD commit, or "" if there is none yet.
func HeadSHA(dir string) string {
	sha, err := out(dir, "rev-parse", "HEAD")
	if err != nil {
		return ""
	}
	return sha
}

// Init initializes a quiet repo in dir.
func Init(dir string) error {
	_, err := out(dir, "init", "-q")
	return err
}

// CheckoutBranch creates or resets branch to HEAD (checkout -B). Errors are
// tolerated by callers that race with a fresh repo.
func CheckoutBranch(dir, branch string) error {
	_, err := out(dir, "checkout", "-q", "-B", branch)
	return err
}

// AddAll stages everything.
func AddAll(dir string) error {
	_, err := out(dir, "add", "-A")
	return err
}

// hasStaged reports whether there are staged changes.
func hasStaged(dir string) bool {
	// `diff --cached --quiet` exits non-zero when there ARE staged changes.
	return !ok(dir, "-C", dir, "diff", "--cached", "--quiet")
}

// withIdentity prepends the fallback author when the repo lacks user.email.
func withIdentity(dir string, args ...string) []string {
	if ok(dir, "-C", dir, "config", "user.email") {
		return args
	}
	return append(append([]string{}, identity...), args...)
}

// Snapshot stages everything and commits with msg if there is anything to
// commit. A no-op (clean tree) is not an error.
func Snapshot(dir, msg string) error {
	if err := AddAll(dir); err != nil {
		return err
	}
	if !hasStaged(dir) {
		return nil
	}
	_, err := out(dir, withIdentity(dir, "commit", "-q", "-m", msg)...)
	return err
}

// RevParse resolves a ref to a commit SHA.
func RevParse(dir, ref string) (string, error) {
	return out(dir, "rev-parse", ref)
}

// CurrentBranch returns the abbreviated current branch name.
func CurrentBranch(dir string) (string, error) {
	return out(dir, "rev-parse", "--abbrev-ref", "HEAD")
}

// ChangedCount returns the number of files changed in base..HEAD.
func ChangedCount(dir, base string) int {
	s, err := out(dir, "diff", "--name-only", base+"..HEAD")
	if err != nil || s == "" {
		return 0
	}
	return len(strings.Split(s, "\n"))
}

// Diff returns the diff for rng (e.g. "base..HEAD"); stat=true returns --stat.
func Diff(dir, rng string, stat bool) (string, error) {
	args := []string{"--no-pager", "diff"}
	if stat {
		args = append(args, "--stat")
	}
	args = append(args, rng)
	return out(dir, args...)
}

// Fetch fetches ref from the given path/remote into FETCH_HEAD.
func Fetch(dir, remote, ref string) error {
	_, err := out(dir, "fetch", "-q", remote, ref)
	return err
}

// RevListCount counts commits in rng (e.g. "base..tip"); 0 on error.
func RevListCount(dir, rng string) int {
	s, err := out(dir, "rev-list", "--count", rng)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(s)
	return n
}

// CherryPick applies rng onto the current branch (using the identity fallback).
func CherryPick(dir, rng string) error {
	_, err := out(dir, withIdentity(dir, "cherry-pick", rng)...)
	return err
}

// CherryPickAbort aborts an in-progress cherry-pick (best effort).
func CherryPickAbort(dir string) {
	_ = ok(dir, "cherry-pick", "--abort")
}

// FormatPatch writes patches for rng into outDir and reports whether any were
// produced.
func FormatPatch(dir, rng, outDir string) (bool, error) {
	s, err := out(dir, "format-patch", rng, "-o", outDir)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(s) != "", nil
}
