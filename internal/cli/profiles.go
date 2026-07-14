package cli

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/config"
)

// newProfileListCmd is the `list` verb of the `profile` group (see profile.go):
// it lists every profile create/enter/exec can resolve by name. By default it
// spans the three sources resolution consults — the project config, the global
// config and the named-profile store — so a profile you can actually use never
// goes missing from the listing. A -f directory narrows it to just that dir.
func newProfileListCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List profiles (project config + global config + store)",
		Long: `List the profiles available to create/enter/exec, across the three sources
they are resolved from, in precedence order:

  project  .sandboxer/config.yaml          (committed, per-project)
  global   ~/.config/sandboxer/config.yaml (your defaults + shared profiles)
  store    ~/.config/sandboxer/profiles/   (one *.yaml per named profile)

A profile's name is its file's base name unless it sets an explicit name:. When
the same name exists in more than one source the higher-precedence one wins
(project > global > store) and the shadowed entries are marked. Use one by name:

  sandboxer create web        # resolves web across the three sources
  sandboxer create web -f ./envs

Pass -f <dir> to instead list a specific directory of profiles.`,
		Example: `  sandboxer profile list            # project + global + store
  sandboxer profile list -f ./envs  # just this directory`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			// -f <dir>: explicit override — list exactly that directory.
			if dir != "" {
				return listDir(out, dir)
			}
			return listAllSources(out)
		},
	}
	cmd.Flags().StringVarP(&dir, "config", "f", "", "list a specific directory of profiles instead of the default sources")
	return cmd
}

// listDir lists the named profiles in a single directory (the -f override).
func listDir(out io.Writer, dir string) error {
	refs := config.ListProfilesIn(dir)
	if len(refs) == 0 {
		fmt.Fprintf(out, "no profiles in %s\n", dir)
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tBACKEND\tFILE")
	for _, r := range refs {
		backend := ""
		if p, err := config.Load(r.Path); err == nil {
			backend = p.Backend
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Name, orDash(backend), r.Path)
	}
	return tw.Flush()
}

// listAllSources lists the project config, global config and store together,
// tagged by source, with the precedence winner marked and shadowed duplicates
// flagged.
func listAllSources(out io.Writer) error {
	entries := config.ListAllProfiles(config.ConfigPath(), config.GlobalConfigPath(), config.ProfilesDir())
	if len(entries) == 0 {
		fmt.Fprintf(out, "no profiles found — scaffold a project profile with 'sandboxer config init', or add <name>.yaml under %s\n", config.ProfilesDir())
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSOURCE\tBACKEND\tFILE")
	shadowed := false
	for _, e := range entries {
		name := e.Name
		// `(default)` is spelled out (not `*`) so the glyph can't be confused
		// with `list`'s active-sandbox marker.
		switch {
		case e.IsDefault:
			name += " (default)"
		case e.Shadowed:
			name += " ~"
			shadowed = true
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", name, e.Source, orDash(e.Backend), e.Path)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if shadowed {
		fmt.Fprintln(out, "\n~ = shadowed by a higher-precedence source")
	}
	return nil
}
