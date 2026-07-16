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
	var detached bool
	cmd := &cobra.Command{
		Use:   "clean [src]",
		Short: "Remove all runtime data for the project (keeps the committed config)",
		Long: `Remove the project's entire runtime state — the sandbox worktrees beside the
project (<project>-sandboxes/) and every agent home, log and metadata file
under the state directory. The committed config (sandboxer.nix) is left
untouched.

--detached limits the sweep to _detached/ — the sources set aside when a
srcs entry was dropped or its branch changed. Live sandboxes stay; branches
are kept (they live in the repos); any uncommitted work still sitting in the
set-aside trees is destroyed — inspect them first (git -C <entry> status).

Requires --force to protect against accidental deletion; use 'sandboxer rm <slug>'
to remove a single sandbox instead.`,
		Example: `  # wipe the project's entire runtime data (config stays)
  sandboxer clean --force

  # only sweep the set-aside dropped sources (_detached/); sandboxes stay
  sandboxer clean --detached --force`,
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
			if detached {
				b, oerr := sandbox.OpenBase(abs)
				if oerr != nil {
					return oerr
				}
				if b == nil {
					fmt.Fprintln(cmd.ErrOrStderr(), "sandboxer: nothing set aside — no sandboxer state here")
					return nil
				}
				removed, cerr := b.CleanDetached()
				for _, r := range removed {
					fmt.Fprintf(cmd.OutOrStdout(), "removed: %s\n", r)
				}
				if cerr != nil {
					return cerr
				}
				if len(removed) == 0 {
					fmt.Fprintln(cmd.ErrOrStderr(), "sandboxer: nothing set aside — _detached/ is empty")
				}
				return nil
			}
			dir := config.StateDir(abs)
			if dir == "" {
				return fmt.Errorf("cannot determine state directory: set $XDG_STATE_HOME or $SANDBOXER_STATE")
			}
			// Sweep the session containers labeled with this state dir first — on
			// every installed engine, since per-profile backends may have created
			// sessions on either; best-effort — the state dir must go even with
			// no engine installed.
			// Collect every source repo the sandboxes span AND remove the
			// worktrees BEFORE the state dir goes: the per-sandbox worktree
			// roots and the repos come from the stored snapshots/meta.
			repos := map[string]bool{}
			var wtRemoved []string
			if b, oerr := sandbox.OpenBase(abs); oerr == nil && b != nil {
				for _, slug := range b.Agents() {
					for _, s := range b.Srcs(slug) {
						if s.Managed {
							repos[s.RepoRoot] = true
						}
					}
				}
				wtRemoved = b.CleanWorktrees()
			} else if p := sandbox.SandboxesRoot(abs); fileExists(p) {
				// No readable state — still sweep the sandboxer-owned default root.
				if os.RemoveAll(p) == nil {
					wtRemoved = append(wtRemoved, p)
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
			// The wiped sandbox dirs held git worktrees; prune their now-dangling
			// admin entries from every source repo. Branches are kept — they live
			// in the repos, not the state dir; delete any by hand.
			for r := range repos {
				_ = worktree.Prune(r)
			}
			for _, p := range wtRemoved {
				fmt.Fprintf(cmd.OutOrStdout(), "removed: %s\n", p)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed: %s\n", dir)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "required to confirm deletion")
	cmd.Flags().BoolVar(&detached, "detached", false, "only remove _detached/ (set-aside dropped sources); live sandboxes stay")
	return cmd
}
