// Package worktree backs a sandbox with a git worktree instead of a copy.
//
// Rather than copying dependency directories into the sandbox and pushing the
// changes back over their origins, sandboxer checks the project repository out
// into a per-sandbox worktree on its own branch (sandbox/<slug>), optionally
// narrowed to a subset of directories via cone-mode sparse-checkout. The
// worktree shares the project's object store, so an agent gets a real git
// (branch, commit, diff) and its work comes back as an ordinary branch — no
// byte copy, no manifest push-back. A project that is not a git repository (or
// has no commit yet) is rejected — there is no copy-mode fallback.
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

// BranchPrefix names the branch a sandbox's worktree is checked out on.
const BranchPrefix = "sandbox/"

// Branch is the branch name for a sandbox slug.
func Branch(slug string) string { return BranchPrefix + slug }

// Detect reports whether dir is inside a git repository that has at least one
// commit, returning the repository's top-level working directory and its shared
// (common) git directory — both absolute. ok is false when dir is not a git
// repo, git is unavailable, or the repo has no HEAD yet (a fresh `git init`
// with no commit): in every such case the caller falls back to the copy path,
// because `git worktree add` needs a commit to branch from. dir may be any
// subdirectory of the repo — the returned top-level is the repo root.
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
	// the caller uses the copy path.
	if _, err := run(dir, "rev-parse", "--verify", "-q", "HEAD"); err != nil {
		return "", "", false
	}
	return strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1]), true
}

// Identity returns the git author identity effective in the repo at dir
// (repo-local config wins over the user's global, exactly as a host commit
// would resolve it). Either value is "" when git has none configured; the caller
// injects what it finds so the agent commits as the developer without writing to
// the sandbox's read-only repo config. Best-effort: a git error yields "", "".
func Identity(dir string) (name, email string) {
	if out, err := run(dir, "config", "--get", "user.name"); err == nil {
		name = strings.TrimSpace(out)
	}
	if out, err := run(dir, "config", "--get", "user.email"); err == nil {
		email = strings.TrimSpace(out)
	}
	return name, email
}

// Ensure makes dest a git worktree of the repo at repoToplevel, checked out on
// branch — created off HEAD when new, reused when it already exists (so a
// recreate keeps the agent's prior commits). When includes is empty the whole
// repo is checked out; otherwise only those repo-relative directories (plus the
// root-level files cone mode always keeps) are materialized via sparse-checkout,
// so `git status` stays clean. dest must not already exist — the caller removes
// it first (git worktree add refuses a non-empty path).
func Ensure(repoToplevel, dest, branch string, includes []string, w io.Writer) error {
	branchExists := false
	if _, err := run(repoToplevel, "rev-parse", "--verify", "-q", "refs/heads/"+branch); err == nil {
		branchExists = true
	}

	add := []string{"worktree", "add"}
	if len(includes) > 0 {
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

	if len(includes) > 0 {
		set := append([]string{"sparse-checkout", "set", "--cone", "--"}, includes...)
		if _, err := run(dest, set...); err != nil {
			return fmt.Errorf("git sparse-checkout: %w", err)
		}
		// The worktree was added --no-checkout; check HEAD out now, honoring the
		// sparse cone just configured.
		if _, err := run(dest, "checkout"); err != nil {
			return fmt.Errorf("git checkout (sparse): %w", err)
		}
	}

	if w != nil {
		scope := "full repo"
		if len(includes) > 0 {
			scope = strings.Join(includes, ", ")
		}
		fmt.Fprintf(w, "worktree %s on %s (%s)\n", branch, filepath.Base(dest), scope)
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
// through git (a copy-mode sandbox dir is just removed).
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
