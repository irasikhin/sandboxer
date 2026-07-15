package cli

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/config"
)

// newProfileListCmd is the `list` verb of the `profile` group (see profile.go):
// it lists the profiles create/enter/exec can resolve by name — the sections
// of ONE config file. That is the project's sandboxer.yaml by default; -f
// points at another file. There is no directory scan and no global store.
func newProfileListCmd() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the config file's profiles",
		Long: `List the profiles available to create/enter/exec: the sections of the
project's ` + config.ConfigPath() + ` (or of the file given with -f). A flat file is its
single profile; a profiles: map lists every section, with the default: marked.
Use one by name:

  sandboxer create web        # the profiles: section named "web"`,
		Example: `  sandboxer profile list            # the project config
  sandboxer profile list -f ./other.yaml`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			path := firstNonEmpty(file, config.ConfigPath())
			entries := config.ListProfiles(path)
			if len(entries) == 0 {
				fmt.Fprintf(out, "no profiles in %s — scaffold one with 'sandboxer config init'\n", path)
				return nil
			}
			return listEntries(out, entries)
		},
	}
	cmd.Flags().StringVarP(&file, "config", "f", "", "config file to list (default: "+config.ConfigPath()+")")
	return cmd
}

// listEntries renders profile rows with the default: marked. `(default)` is
// spelled out (not `*`) so the glyph can't be confused with `list`'s
// active-sandbox marker.
func listEntries(out io.Writer, entries []config.ProfileEntry) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tBACKEND\tFILE")
	for _, e := range entries {
		name := e.Name
		if e.IsDefault {
			name += " (default)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", name, orDash(e.Backend), e.Path)
	}
	return tw.Flush()
}
