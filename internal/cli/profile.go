package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/config"
)

func init() { register(newProfileCmd) }

// newProfileCmd groups the profile/config verbs under `sandboxer profile`:
// selecting the active profile, scaffolding/editing/validating the project
// config, and listing the named-profile store. This is the "form the config"
// half of setup; `use` is also kept as a top-level alias for the common case.
func newProfileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Select, scaffold, edit, validate and list profiles",
		Long: `Work with profiles — the per-project config that drives a sandbox.

  sandboxer profile use [slug]   select/show the active sandbox (alias of 'use')
  sandboxer profile init         scaffold a commented .sandboxer/config.yaml
  sandboxer profile edit         edit it in $EDITOR
  sandboxer profile validate     check it parses (unknown fields are errors)
  sandboxer profile list         list profiles (project + global + store)`,
	}
	cmd.AddCommand(
		newUseCmd(),
		newProfileInitCmd(),
		newProfileEditCmd(),
		newProfileValidateCmd(),
		newProfileListCmd(),
	)
	return cmd
}

// newProfileEditCmd opens .sandboxer/config.yaml in $EDITOR, scaffolding the
// fully-annotated starter config first when the file does not exist — so a user
// always edits a concrete, documented file rather than a blank one.
func newProfileEditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "edit",
		Short: "Edit " + config.ConfigPath() + " in $EDITOR (scaffolds it if missing)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := config.ConfigPath()
			if !fileExists(path) {
				if err := os.MkdirAll(config.StateDirName, 0o755); err != nil {
					return err
				}
				name := config.Sanitize(filepath.Base(getwd()))
				if name == "" {
					name = "feat"
				}
				if err := os.WriteFile(path, []byte(starterProfile(name, config.LoadDefaults())), 0o644); err != nil {
					return err
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "sandboxer: scaffolded %s\n", path)
			}
			return openInEditor(cmd, path)
		},
	}
	return cmd
}

// newProfileValidateCmd parses a profile config through the strict decoder
// (config.LoadDocument with KnownFields(true)), so a typo'd field name is
// reported as an error here instead of being silently ignored at run time. This
// gives "is my config valid, and what's wrong?" a precise, dedicated answer.
func newProfileValidateCmd() *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "validate [file]",
		Short: "Validate a profile config (strict: unknown fields are errors)",
		Long: `Parse a profile config strictly and report the first problem precisely.

Unknown field names are rejected (not silently ignored), so a typo like
'allowedDomain' surfaces here rather than quietly doing nothing at run time.
Defaults to ` + config.ConfigPath() + `; pass a file or -f to check another.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := firstNonEmpty(configPath, posArg(args), config.ConfigPath())
			if !fileExists(path) {
				return fmt.Errorf("no config at %s (scaffold one: sandboxer profile init)", path)
			}
			if _, err := config.LoadDocument(path); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: ok\n", path)
			return nil
		},
	}
	cmd.Flags().StringVarP(&configPath, "config", "f", "", "config file to validate (default: "+config.ConfigPath()+")")
	return cmd
}
