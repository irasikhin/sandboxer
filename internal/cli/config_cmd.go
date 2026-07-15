package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/config"
)

func init() { register(newConfigCmd) }

// newConfigCmd groups the config-file verbs under `sandboxer config`. The
// config is ONE nix file (sandboxer.nix), so the surface is deliberately
// small: scaffold it, open it in the editor, check it evaluates. Point edits
// happen in the editor — nix has no comment-preserving programmatic editing,
// and the file IS the source of truth.
func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Scaffold, edit and validate the config file",
		Long: `Work with the config file — the committed ` + config.ConfigPath() + `.

  sandboxer config init       scaffold a commented ` + config.ConfigPath() + `
  sandboxer config edit       edit it in $EDITOR (scaffolds it if missing)
  sandboxer config validate   evaluate it and check the schema strictly

The file must evaluate (restricted nix eval: no network, no reads outside its
directory) to one profile attrset, or to
{ profiles = { <name> = { ... }; }; default = "<name>"; }.`,
	}
	cmd.AddCommand(
		newConfigInitCmd(),
		newConfigEditCmd(),
		newConfigValidateCmd(),
	)
	return cmd
}

// newConfigEditCmd opens sandboxer.nix in $EDITOR, scaffolding the
// fully-annotated starter config first when the file does not exist — so a
// user always edits a concrete, documented file rather than a blank one.
func newConfigEditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "edit",
		Short: "Edit " + config.ConfigPath() + " in $EDITOR (scaffolds it if missing)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := config.ConfigPath()
			if !fileExists(path) {
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

// newConfigValidateCmd evaluates a config and decodes it through the strict
// schema (unknown attrs are errors, with removed-key migration hints), so a
// typo'd attr name is reported here instead of being silently ignored at run
// time. This gives "is my config valid, and what's wrong?" a precise,
// dedicated answer.
func newConfigValidateCmd() *cobra.Command {
	var configPath string
	cmd := &cobra.Command{
		Use:   "validate [file]",
		Short: "Validate a config (strict: unknown attrs are errors)",
		Long: `Evaluate a config with nix and check it against the schema strictly.

Unknown attr names are rejected (not silently ignored), so a typo like
'allowedDomain' surfaces here rather than quietly doing nothing at run time.
Defaults to ` + config.ConfigPath() + `; pass a file or -f to check another.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := firstNonEmpty(configPath, posArg(args), config.ConfigPath())
			if !fileExists(path) {
				return fmt.Errorf("no config at %s (scaffold one: sandboxer config init)", path)
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
