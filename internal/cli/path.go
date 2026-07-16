package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func init() { register(newPathCmd) }

// newPathCmd exposes the resolved source worktree paths on stdout. enter
// already reports them, but only as stderr diagnostics in the moment: they
// scroll away and cannot be substituted into a command. path is the composable
// counterpart — the state dir is hashed per project, so without it the paths
// are effectively unfindable by hand.
func newPathCmd() *cobra.Command {
	var src string
	var dir bool
	cmd := &cobra.Command{
		Use:   "path [slug]",
		Short: "Print the host path of a sandbox's source worktrees",
		Long: `Print the absolute host path of each source worktree in a sandbox, one per
line, in srcs order — the same paths 'enter' reports, but on stdout and without
entering anything, so they compose with your own tooling:

  code "$(sandboxer path)"          # open the active sandbox in your editor
  git -C "$(sandboxer path)" diff   # review the agent's work on the host

With no slug the active sandbox is used (see 'sandboxer use'). --dir prints the
sandbox's mount root (<state>/<slug>/) instead — the parent the container sees,
holding every managed worktree.

sandboxer does not open an editor for you (as it does not manage your tmux): it
prints the path and leaves the tool choice to you.`,
		Example: `  # the active sandbox's worktree
  sandboxer path

  # every source of the sandbox "feat", one per line
  sandboxer path feat

  # the mount root instead of the worktrees
  sandboxer path feat --dir`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := resolveTarget(commonFlags{src: src}, posArg(args))
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if dir {
				fmt.Fprintln(out, t.base.SandboxDir(t.slug))
				return nil
			}
			srcs := t.base.Srcs(t.slug)
			if len(srcs) == 0 {
				return fmt.Errorf("no sources recorded for sandbox %q — materialize them with "+
					"'sandboxer enter %s', or ask for the mount root: sandboxer path %s --dir",
					t.slug, t.slug, t.slug)
			}
			for _, s := range srcs {
				fmt.Fprintln(out, s.Path)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&src, "src", "", "project root (default: cwd)")
	cmd.Flags().BoolVar(&dir, "dir", false, "print the sandbox mount root instead of the source worktrees")
	return cmd
}
