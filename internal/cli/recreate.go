package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/worktree"
)

func init() { register(newRecreateCmd) }

func newRecreateCmd() *cobra.Command {
	var f commonFlags
	var full bool
	var force bool
	cmd := &cobra.Command{
		Use:   "recreate [slug|profile|file.nix]",
		Short: "Re-create a sandbox from scratch (keeps the agent home; --full also drops minted branches)",
		Long: `Tear a sandbox down and build it again from the profile. Every managed
source worktree is removed and re-created off HEAD, reusing its branch so prior
COMMITS survive — but UNCOMMITTED work in a worktree is force-removed, so
recreate refuses when any source has local changes unless you pass --force
(commit first to keep them). The setup script re-runs on the next enter. The
private agent home (_home/<slug> — logins, shell history) is preserved so
agents need no re-authentication; --full removes it too AND drops the branches
sandboxer MINTED for this sandbox (those that did not already exist when it was
first created; a branch that pre-existed is always kept), making recreate a
full reset.`,
		Example: `  # rebuild the active sandbox's working copy, keep agent logins
  sandboxer recreate

  # full reset, agent home included (re-login required)
  sandboxer recreate feat --full`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := resolveTarget(f, posArg(args))
			if err != nil {
				return err
			}
			if t.profile == nil {
				return fmt.Errorf("no profile for %q — scaffold one with 'sandboxer config init', then recreate", t.slug)
			}
			// recreate force-removes each managed worktree and re-checks it out off
			// HEAD: branches (and their commits) survive, but UNCOMMITTED work does
			// not. Refuse when any source has local changes unless --force, so a
			// rebuild never silently discards edits (mirrors reset).
			var dirty []string
			for _, s := range t.base.Srcs(t.slug) {
				if s.Managed && worktree.IsWorktree(s.Path) && worktree.HasWork(s.Path) {
					dirty = append(dirty, filepath.Base(s.RepoRoot))
				}
			}
			if len(dirty) > 0 && !force {
				return fmt.Errorf("uncommitted work in: %s — commit it (it survives on the branch), "+
					"or pass --force to discard and rebuild", strings.Join(dirty, ", "))
			}
			// Capture the stored snapshot and the active marker before the wipe:
			// MakeSandbox below needs a profile snapshot even when the target was
			// resolved from the stored profile.json (t.json == nil), and --full's
			// Remove clears the active marker.
			storedJSON, _ := os.ReadFile(t.base.ProfileJSONPath(t.slug))
			wasCurrent := t.base.Current() == t.slug
			// The session container goes first, while the engine labels still match
			// an existing base dir; best-effort — an engine-less host must still get
			// its sandbox rebuilt.
			removeSessionBestEffort(t, f, cmd.ErrOrStderr())
			if full {
				// Capture the recorded sources before Remove wipes the meta; the
				// branches can only be deleted once their worktrees are gone.
				srcs := t.base.Srcs(t.slug)
				if err := t.base.Remove(t.slug); err != nil {
					return err
				}
				// A full reset starts from a clean slate: drop the auto-named
				// sandbox branches too, so MakeSandbox re-branches off HEAD. A
				// normal recreate keeps them, reusing the agent's prior commits.
				t.base.RemoveSandboxBranches(t.slug, srcs)
			} else {
				t.base.RemoveState(t.slug, true)
			}
			snapshot := t.json
			if snapshot == nil {
				snapshot = storedJSON
			}
			if len(snapshot) > 0 {
				if err := t.base.WriteProfileJSON(t.slug, snapshot); err != nil {
					return err
				}
			}
			// Refresh remote sources from origin (recreate is the clone-once
			// refresh point). Best-effort: a fetch failure keeps the cached copy
			// and still rebuilds, so an offline recreate works.
			_ = t.base.RefreshRemotes(t.slug, cmd.ErrOrStderr())
			if err := t.base.MakeSandbox(t.slug, cmd.ErrOrStderr()); err != nil {
				return err
			}
			if wasCurrent {
				if err := t.base.SetCurrent(t.slug); err != nil {
					return err
				}
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "sandbox %q recreated: %s\n", t.slug, t.base.SandboxDir(t.slug))
			if full {
				fmt.Fprintln(out, "agent home wiped (--full) — agents must re-authenticate")
			}
			return nil
		},
	}
	bindExisting(cmd, &f)
	cmd.Flags().BoolVar(&full, "full", false, "also wipe the private agent home (full rm+create)")
	cmd.Flags().BoolVar(&force, "force", false, "discard uncommitted work in the worktrees (recreate force-removes them)")
	return cmd
}
