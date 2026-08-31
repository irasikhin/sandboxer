package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/sandbox"
	"github.com/irasikhin/sandboxer/internal/style"
	"github.com/irasikhin/sandboxer/internal/worktree"
)

func init() { register(newResetCmd) }

// newResetCmd re-bases a sandbox's source branch(es) onto a fresh base — the
// host-side "the PR merged, start the next change from the updated default".
// It is the ergonomic wrapper over `git -C "$(sandboxer path …)" fetch && reset
// --hard`; sandboxer owns the worktrees, so resetting one is a first-class
// lifecycle op beside rm/clean, but the reset itself is still plain git.
func newResetCmd() *cobra.Command {
	var f commonFlags
	var onto string
	var force bool
	var noFetch bool
	cmd := &cobra.Command{
		Use:   "reset [slug] [source]",
		Short: "Re-base a sandbox's source branch onto its merged base",
		Long: `Re-base a sandbox's source branch onto a fresh base — for continuing after a
merged PR whose remote branch is gone. For each managed source it fetches
origin, then 'git reset --hard' moves the source's branch (staying ON it) onto
the base, so a live session picks up the new base immediately (no recreate).

With no source, every managed source is reset — each onto <base> in its OWN
repo; name a source to reset just that one. Adopted worktrees (a branch checked
out elsewhere) are skipped: sandboxer does not own them.

The base is origin/main by default; override with --onto <ref> (e.g. a repo
whose default branch is master: --onto origin/master). 'reset --hard' discards
uncommitted work AND abandons any commits the branch has beyond the base
(they remain only in the reflog), so a source with local changes or un-merged
commits is refused unless --force — and the whole sandbox is checked before
anything is reset, so one dirty source never leaves you half-reset.`,
		Example: `  # reset every source of "feat" onto origin/main
  sandboxer reset feat

  # just the "api" source, onto a different base
  sandboxer reset feat api --onto origin/master

  # discard local edits and reset the active sandbox
  sandboxer reset --force`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := resolveTarget(f, posArg(args))
			if err != nil {
				return err
			}
			srcs := t.base.Srcs(t.slug)
			if len(srcs) == 0 {
				return fmt.Errorf("no sources recorded for sandbox %q — materialize them with "+
					"'sandboxer enter %s'", t.slug, t.slug)
			}
			targets := srcs
			if len(args) > 1 {
				s, err := sandbox.FindSource(srcs, args[1])
				if err != nil {
					return err
				}
				targets = []sandbox.Source{s}
			}

			base := onto
			if base == "" {
				base = "origin/main"
			}
			out := cmd.OutOrStdout()
			errOut := cmd.ErrOrStderr()

			// Pass 1 — fetch and pre-flight every target, so a dirty source or an
			// unresolvable base aborts BEFORE any branch is moved (no half-reset).
			var managed []sandbox.Source
			var dirty, ahead []string
			for _, s := range targets {
				if !s.Managed {
					style.Warnf(errOut, "source %s skipped — adopted worktree, sandboxer does not reset it", s.Name())
					continue
				}
				if !noFetch {
					if err := worktree.Fetch(s.Path); err != nil {
						return fmt.Errorf("source %s: %w", s.Name(), err)
					}
				}
				if worktree.ShortHash(s.Path, base) == "" {
					return fmt.Errorf("source %s: cannot resolve base %q — fetch first (drop --no-fetch) "+
						"or name one with --onto <ref>", s.Name(), base)
				}
				clean, err := worktree.IsClean(s.Path)
				if err != nil {
					return fmt.Errorf("source %s: %w", s.Name(), err)
				}
				if !clean && !force {
					dirty = append(dirty, s.Name())
				}
				// reset --hard also abandons commits the branch has beyond the base
				// (the branch's PR hasn't merged yet). IsClean only sees the working
				// tree, so guard those un-merged commits explicitly.
				n, err := worktree.Ahead(s.Path, base)
				if err != nil {
					return fmt.Errorf("source %s: %w", s.Name(), err)
				}
				if n > 0 && !force {
					ahead = append(ahead, fmt.Sprintf("%s (%d commit(s))", s.Name(), n))
				}
				managed = append(managed, s)
			}
			if len(dirty) > 0 {
				return fmt.Errorf("uncommitted changes in: %s — commit or stash them, "+
					"or pass --force to discard (reset --hard)", strings.Join(dirty, ", "))
			}
			if len(ahead) > 0 {
				return fmt.Errorf("un-merged commits would be abandoned in: %s — the branch is ahead of %s "+
					"(has the PR merged?); push/merge first, or pass --force to reset anyway "+
					"(the old commits stay in the reflog)", strings.Join(ahead, ", "), base)
			}
			if len(managed) == 0 {
				style.Infof(errOut, "nothing to reset (no managed sources)")
				return nil
			}

			// Pass 2 — move each branch onto the base (pre-flighted, so this holds).
			for _, s := range managed {
				if err := worktree.ResetHard(s.Path, base); err != nil {
					return fmt.Errorf("source %s: %w", s.Name(), err)
				}
				fmt.Fprintf(out, "reset: %s %s → %s (%s)\n", s.Name(), s.Branch, base, worktree.ShortHash(s.Path, "HEAD"))
			}
			return nil
		},
	}
	// reset never starts a container — it fetches and re-bases the host
	// worktrees — so it takes the resolution flags only.
	bindTarget(cmd, &f)
	cmd.Flags().StringVar(&onto, "onto", "", "base ref to reset onto (default: origin/main)")
	cmd.Flags().BoolVar(&force, "force", false, "reset even with uncommitted changes or un-merged commits (discards them)")
	cmd.Flags().BoolVar(&noFetch, "no-fetch", false, "skip 'git fetch' (use already-fetched refs)")
	return cmd
}
