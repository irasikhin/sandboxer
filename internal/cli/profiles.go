package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/config"
)

func init() { register(newProfilesCmd) }

func newProfilesCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "profiles",
		Short: "List named profiles (the global store, or a -f directory)",
		Long: `List the named profiles available to create/enter/exec/run.

A profile is a YAML file; its name is the file's base name unless it sets an
explicit name:. The global store is ~/.config/sandboxer/profiles (override with
$SANDBOXER_PROFILES or $XDG_CONFIG_HOME). Use one by name, e.g.:

  sandboxer create web        # ~/.config/sandboxer/profiles/web.yaml
  sandboxer create web -f ./envs`,
		Example: `  sandboxer profiles            # the global store
  sandboxer profiles -f ./envs  # a directory of profiles`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := dir
			if d == "" {
				d = config.ProfilesDir()
			}
			if d == "" {
				return fmt.Errorf("no profiles directory (set SANDBOXER_PROFILES or XDG_CONFIG_HOME)")
			}
			out := cmd.OutOrStdout()
			refs := config.ListProfilesIn(d)
			if len(refs) == 0 {
				fmt.Fprintf(out, "no profiles in %s\n", d)
				return nil
			}
			fmt.Fprintf(out, "profiles in %s:\n", d)
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tBACKEND\tAGENT\tFILE")
			for _, r := range refs {
				backend, agent := "", ""
				if p, err := config.Load(r.Path); err == nil {
					backend, agent = p.Backend, p.Agent
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Name, orDash(backend), orDash(agent), r.Path)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVarP(&dir, "config", "f", "", "profiles directory (default: the global store)")
	return cmd
}
