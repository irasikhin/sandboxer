package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/sandbox"
	"github.com/irasikhin/sandboxer/internal/worktree"
)

func init() { register(newCleanCmd) }

// newCleanCmd removes ALL runtime data for a project (replacing the old
// rm-all): the entire state directory — sandboxes, agent homes, logs, metadata
// — which now lives under config.StateDir, outside the repo. The committed
// config (sandboxer.nix) is deliberately left untouched,
// which is the whole point of the config/data split.
func newCleanCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "clean [src]",
		Short: "Remove all runtime data for the project (keeps the committed config)",
		Long: `Remove the project's entire runtime state — the sandbox worktrees beside the
project (<project>-sandboxes/) and every agent home, log and metadata file
under the state directory. The committed config (sandboxer.nix) is left
untouched.

Requires --force to protect against accidental deletion; use 'sandboxer rm <slug>'
to remove a single sandbox instead.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				return fmt.Errorf("clean requires --force; use 'sandboxer rm <slug>' to remove a single sandbox")
			}
			src := firstNonEmpty(posArg(args), getwd())
			abs, err := filepath.Abs(src)
			if err != nil {
				return fmt.Errorf("no such directory: %s", src)
			}
			dir := config.StateDir(abs)
			if dir == "" {
				return fmt.Errorf("cannot determine state directory: set $XDG_STATE_HOME or $SANDBOXER_STATE")
			}
			// Sweep the session containers labeled with this state dir first — on
			// every installed engine, since per-profile backends may have created
			// sessions on either; best-effort — the state dir must go even with
			// no engine installed.
			// Collect every source repo the sandboxes span BEFORE the wipe, so
			// their then-dangling worktree admin entries can be pruned after it.
			repos := map[string]bool{}
			if b, oerr := sandbox.OpenBase(abs); oerr == nil && b != nil {
				for _, slug := range b.Agents() {
					for _, s := range b.Srcs(slug) {
						if s.Managed {
							repos[s.RepoRoot] = true
						}
					}
				}
			}
			if top, _, ok := worktree.Detect(abs); ok {
				repos[top] = true // pre-srcs-model sandboxes lived on the project repo
			}
			engines := backendInstalledEngines(config.LoadDefaults())
			if len(engines) == 0 {
				fmt.Fprintln(cmd.ErrOrStderr(),
					"sandboxer: session cleanup skipped: no container engine (docker or podman) found")
			}
			for _, engine := range engines {
				if err := backendRemoveAllSessions(engine, dir); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "sandboxer: session cleanup failed: %v\n", err)
				}
			}
			if err := os.RemoveAll(dir); err != nil {
				return err
			}
			// The worktrees live beside the project, separate from the state dir.
			sbRoot := sandbox.SandboxesRoot(abs)
			if err := os.RemoveAll(sbRoot); err != nil {
				return err
			}
			// The wiped sandbox dirs held git worktrees; prune their now-dangling
			// admin entries from every source repo. Branches are kept — they live
			// in the repos, not the state dir; delete any by hand.
			for r := range repos {
				_ = worktree.Prune(r)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed: %s\n", sbRoot)
			fmt.Fprintf(cmd.OutOrStdout(), "removed: %s\n", dir)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "required to confirm deletion")
	return cmd
}
