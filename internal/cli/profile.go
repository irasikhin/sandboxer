package cli

import (
	"github.com/spf13/cobra"
)

func init() { register(newProfileCmd) }

// newProfileCmd groups the profile-selection verbs under `sandboxer profile`:
// picking the active sandbox and listing the profiles a sandbox can be
// created from. The config FILE verbs (init/edit/validate/get/set/unset)
// live under `sandboxer config`; the old `profile init|edit|validate`
// spellings remain as hidden deprecated aliases for one release.
func newProfileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Select and list profiles",
		Long: `Work with profiles — the named sections a sandbox is created from
(they all live in one config file, under its profiles: map).

  sandboxer profile use [slug]   select/show the active sandbox (alias of 'use')
  sandboxer profile list         list the config file's profiles

The config file itself is managed by 'sandboxer config'
(init/edit/validate/get/set/unset).`,
	}
	cmd.AddCommand(
		newUseCmd(),
		newProfileListCmd(),
		deprecatedAlias(newConfigInitCmd(), "sandboxer config init"),
		deprecatedAlias(newConfigEditCmd(), "sandboxer config edit"),
		deprecatedAlias(newConfigValidateCmd(), "sandboxer config validate"),
	)
	return cmd
}

// deprecatedAlias marks a fresh command instance as a hidden, deprecated
// alias of its new home: cobra prints the notice and still runs it, and
// Hidden keeps it out of --help. Callers pass a NEW instance — a
// *cobra.Command cannot have two parents.
func deprecatedAlias(c *cobra.Command, instead string) *cobra.Command {
	c.Deprecated = "use '" + instead + "' instead"
	c.Hidden = true
	return c
}
