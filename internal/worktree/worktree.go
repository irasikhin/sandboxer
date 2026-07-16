// Package worktree backs a sandbox source with a host-side git worktree.
//
// Each source repository is checked out into a per-sandbox worktree on its own
// branch (feat/<slug> by default), optionally narrowed to a subset of files
// via non-cone sparse-checkout with gitignore-syntax include patterns. The
// worktree is the containment boundary: only its (sparse) contents are mounted
// into the container — git metadata never is — so work accumulates in the
// worktree and returns as an ordinary branch via HOST-side git. A source that
// is not a git repository (or has no commit yet) is rejected — there is no
// copy-mode fallback.
//
// Everything here is thin orchestration over the git CLI; git is expected on
// PATH (Detect reports the repo as absent when it is not, so the caller falls
// back cleanly).
package worktree

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// BranchPrefix names the branch a sandbox's worktree is checked out on:
// feat/<slug>. An EXISTING feat/<slug> branch of yours is deliberately
// reused — checked out into the managed worktree, or adopted where it is
// already checked out; recreate --full only ever deletes branches sandboxer
// itself created (recorded per source at first sync).
const BranchPrefix = "feat/"

// Branch is the branch name for a sandbox slug.
func Branch(slug string) string { return BranchPrefix + slug }

// BranchExists reports whether the repo already has a local branch by that
// name — the callers use it to record which branches sandboxer MINTED (only
// those are deleted on a full reset).
func BranchExists(repoToplevel, branch string) bool {
	_, err := run(repoToplevel, "rev-parse", "--verify", "-q", "refs/heads/"+branch)
	return err == nil
}

// Detect reports whether dir is inside a git repository that has at least one
// commit, returning the repository's top-level working directory and its shared
// (common) git directory — both absolute. ok is false when dir is not a git
// repo, git is unavailable, or the repo has no HEAD yet (a fresh `git init`
// with no commit) — `git worktree add` needs a commit to branch from, so the
// caller rejects such a source with a hint. dir may be any subdirectory of the
// repo — the returned top-level is the repo root.
func Detect(dir string) (toplevel, commonDir string, ok bool) {
	out, err := run(dir, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir")
	if err != nil {
		return "", "", false
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) == "" || strings.TrimSpace(lines[1]) == "" {
		return "", "", false
	}
	// A repo with no commits cannot be branched from — treat it as non-git so
	// the caller rejects it with an actionable hint.
	if _, err := run(dir, "rev-parse", "--verify", "-q", "HEAD"); err != nil {
		return "", "", false
	}
	return strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1]), true
}

// wholeRepo reports whether the include patterns select the whole repository —
// no patterns, or the single catch-all "**" — so no sparse-checkout is needed.
func wholeRepo(include []string) bool {
	return len(include) == 0 || (len(include) == 1 && include[0] == "**")
}

// Ensure makes dest a git worktree of the repo at repoToplevel, checked out on
// branch — created off HEAD when new, reused when it already exists (so a
// recreate keeps the agent's prior commits). include narrows what is
// materialized via non-cone sparse-checkout with gitignore-syntax patterns
// (last match wins, "!…" negates); empty (or ["**"]) checks out the whole
// repo. dest must not already exist — the caller removes it first (git
// worktree add refuses a non-empty path).
func Ensure(repoToplevel, dest, branch string, include []string, w io.Writer) error {
	branchExists := false
	if _, err := run(repoToplevel, "rev-parse", "--verify", "-q", "refs/heads/"+branch); err == nil {
		branchExists = true
	}

	add := []string{"worktree", "add"}
	if !wholeRepo(include) {
		add = append(add, "--no-checkout")
	}
	if branchExists {
		add = append(add, dest, branch)
	} else {
		add = append(add, "-b", branch, dest, "HEAD")
	}
	if _, err := run(repoToplevel, add...); err != nil {
		return fmt.Errorf("git worktree add: %w", err)
	}

	if !wholeRepo(include) {
		set := append([]string{"sparse-checkout", "set", "--no-cone", "--"}, include...)
		if _, err := run(dest, set...); err != nil {
			return fmt.Errorf("git sparse-checkout: %w", err)
		}
		// The worktree was added --no-checkout; check HEAD out now, honoring the
		// sparse patterns just configured.
		if _, err := run(dest, "checkout"); err != nil {
			return fmt.Errorf("git checkout (sparse): %w", err)
		}
	}

	if w != nil {
		fmt.Fprintf(w, "worktree %s on %s (%s)\n", branch, filepath.Base(dest), scopeLabel(include))
	}
	return nil
}

// scopeLabel renders the include patterns for progress lines.
func scopeLabel(include []string) string {
	if wholeRepo(include) {
		return "full repo"
	}
	return strings.Join(include, ", ")
}

// SyncSparse converges an existing worktree's sparse-checkout onto the include
// patterns: narrowing, widening or disabling as needed, in place. It is
// idempotent and cheap when nothing changed. Pattern ORDER matters (gitignore
// semantics: last match wins), so the comparison is order-sensitive. Locally
// modified files in paths that fall out of the selection are left on disk by
// git — nothing is lost. changed reports whether the working tree was touched.
func SyncSparse(dest string, include []string, w io.Writer) (changed bool, err error) {
	current := sparseList(dest)
	if wholeRepo(include) {
		if current == nil {
			return false, nil // already a full checkout
		}
		if _, err := run(dest, "sparse-checkout", "disable"); err != nil {
			return false, fmt.Errorf("git sparse-checkout disable: %w", err)
		}
	} else {
		if slicesEqual(current, include) {
			return false, nil
		}
		set := append([]string{"sparse-checkout", "set", "--no-cone", "--"}, include...)
		if _, err := run(dest, set...); err != nil {
			return false, fmt.Errorf("git sparse-checkout: %w", err)
		}
	}
	if w != nil {
		fmt.Fprintf(w, "worktree %s resynced (%s)\n", filepath.Base(dest), scopeLabel(include))
	}
	return true, nil
}

// sparseList returns the worktree's current sparse-checkout patterns, or nil
// when sparse-checkout is not active (a full checkout). Errors read as "not
// sparse" — SyncSparse then converges from scratch.
func sparseList(dest string) []string {
	if out, err := run(dest, "config", "--worktree", "core.sparseCheckout"); err != nil || strings.TrimSpace(out) != "true" {
		return nil
	}
	out, err := run(dest, "sparse-checkout", "list")
	if err != nil {
		return nil
	}
	var patterns []string
	for _, line := range strings.Split(out, "\n") {
		if l := strings.TrimSpace(line); l != "" {
			patterns = append(patterns, l)
		}
	}
	return patterns
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// FindWorktree reports where branch is currently checked out — any worktree of
// the repo, including its main checkout — so a srcs entry naming an existing
// branch adopts that checkout instead of failing git's one-worktree-per-branch
// rule. ok is false when the branch is not checked out anywhere (or git
// fails).
func FindWorktree(repoToplevel, branch string) (path string, ok bool) {
	out, err := run(repoToplevel, "worktree", "list", "--porcelain")
	if err != nil {
		return "", false
	}
	want := "refs/heads/" + branch
	cur := ""
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			cur = strings.TrimPrefix(line, "worktree ")
		case strings.TrimSpace(line) == "branch "+want && cur != "":
			return cur, true
		}
	}
	return "", false
}

// CurrentBranch returns the branch a worktree is on ("" for a detached HEAD or
// on error). Used to notice that a managed worktree's configured branch
// changed, so the old tree can be set aside and a fresh one checked out.
func CurrentBranch(dest string) string {
	out, err := run(dest, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	b := strings.TrimSpace(out)
	if b == "HEAD" {
		return ""
	}
	return b
}

// Move relocates a worktree's working directory (git worktree move), keeping
// its checkout, branch and any uncommitted changes intact. Used to set a
// dropped source aside under _detached/ instead of destroying its work.
func Move(repoToplevel, from, to string) error {
	if _, err := run(repoToplevel, "worktree", "move", from, to); err != nil {
		return fmt.Errorf("git worktree move: %w", err)
	}
	return nil
}

// Remove deletes the worktree at dest and prunes its administrative entry from
// the shared repo. The branch is kept, so the agent's work survives the
// teardown; delete it explicitly with DeleteBranch. Best-effort: a dest git no
// longer tracks is still removed from disk and its stale entry pruned.
func Remove(repoToplevel, dest string) error {
	if _, err := run(repoToplevel, "worktree", "remove", "--force", dest); err != nil {
		// The admin entry may be stale (dir already gone, or never a worktree);
		// make sure the working dir is gone, then prune the entry below.
		_ = os.RemoveAll(dest)
	}
	_, err := run(repoToplevel, "worktree", "prune")
	return err
}

// DeleteBranch force-deletes a sandbox branch. It is best-effort from the
// caller's side (a missing branch is not worth failing a teardown over).
func DeleteBranch(repoToplevel, branch string) error {
	if _, err := run(repoToplevel, "branch", "-D", branch); err != nil {
		return fmt.Errorf("git branch -D %s: %w", branch, err)
	}
	return nil
}

// Prune removes administrative entries for worktrees whose working directories
// are gone (e.g. after `clean` wiped the state dir). It never touches a live
// worktree or any branch.
func Prune(repoToplevel string) error {
	_, err := run(repoToplevel, "worktree", "prune")
	return err
}

// IsWorktree reports whether dest is a git worktree — it has a .git entry that
// resolves inside a work tree. Used so a teardown only routes a real worktree
// through git (anything else is just removed).
func IsWorktree(dest string) bool {
	if _, err := os.Lstat(filepath.Join(dest, ".git")); err != nil {
		return false
	}
	_, err := run(dest, "rev-parse", "--is-inside-work-tree")
	return err == nil
}

// run executes `git -C dir args...`, returning stdout; on failure the error
// carries git's stderr so the caller (and the user) sees why it failed.
func run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", errors.New(msg)
		}
		return "", err
	}
	return stdout.String(), nil
}
