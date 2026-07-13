package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func init() { register(newRecreateCmd) }

func newRecreateCmd() *cobra.Command {
	var f commonFlags
	var full bool
	cmd := &cobra.Command{
		Use:   "recreate [slug|profile|file.yaml]",
		Short: "Re-create a sandbox from scratch (keeps the agent home; --full also drops the branch)",
		Long: `Tear a sandbox down and build it again from the profile. The worktree is
removed and re-created off HEAD, reusing the sandbox branch so the agent's prior
commits survive. The setup script re-runs on the next
enter. The private agent home (_home/<slug> — logins, shell history) is preserved
so agents need no re-authentication; --full removes it too AND drops the sandbox
branch, making recreate a full reset.`,
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
				return fmt.Errorf("no profile for %q — scaffold one with 'sandboxer init', then recreate", t.slug)
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
				if err := t.base.Remove(t.slug); err != nil {
					return err
				}
				// A full reset starts from a clean slate: drop the sandbox branch too
				// (git mode) so MakeSandbox re-branches off HEAD. A normal recreate
				// keeps the branch, so the agent's prior commits are reused.
				t.base.RemoveSandboxBranch(t.slug)
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
	return cmd
}
