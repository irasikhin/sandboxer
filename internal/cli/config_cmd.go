package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

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
Each profile then gets the same static semantic checks create/enter run —
backend and session names, domains, proxy, routes, include-pattern shapes,
srcs with their required branches — so a config that validates can only
still fail on what needs the repo on disk (a branch that does not exist, a
pattern matching nothing).
Defaults to ` + config.ConfigPath() + `; pass a file or -f to check another.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := firstNonEmpty(configPath, posArg(args), config.ConfigPath())
			if !fileExists(path) {
				return fmt.Errorf("no config at %s (scaffold one: sandboxer config init)", path)
			}
			doc, err := config.LoadDocument(path)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			names := make([]string, 0, len(doc.Profiles))
			for name := range doc.Profiles {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				if err := validateProfile(doc.Profiles[name]); err != nil {
					if doc.Multi() {
						return fmt.Errorf("%s: profiles.%s: %w", path, name, err)
					}
					return fmt.Errorf("%s: %w", path, err)
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: ok (%d profile(s))\n", path, len(names))
			return nil
		},
	}
	cmd.Flags().StringVarP(&configPath, "config", "f", "", "config file to validate (default: "+config.ConfigPath()+")")
	return cmd
}

// validateProfile runs the static semantic checks on one profile — the same
// validators create/enter run, minus anything needing the repo on disk. The
// srcs shape checks live here rather than in config.ValidateSrcs: read-only
// commands (show, stop, compose) legitimately resolve a nil/empty profile,
// and on enter/create the sandbox layer's richer errors (the recorded-branch
// hint) must not be shadowed by a generic one.
func validateProfile(p config.Profile) error {
	if len(p.Srcs) == 0 {
		return fmt.Errorf("srcs is empty — a sandbox needs at least one source, e.g. srcs = [ { src = \".\"; branch = \"devops/my-change\"; } ]")
	}
	for _, s := range p.Srcs {
		if s.Src == "" {
			return fmt.Errorf("srcs entry: src is required — a repo path or git URL")
		}
		if s.Branch == "" {
			return fmt.Errorf("srcs entry %q: branch is required — every source names its branch explicitly, e.g. { src = %q; branch = \"devops/my-change\"; }", s.Src, s.Src)
		}
	}
	// LoadDefaults, not a zero Defaults: validate must judge the config the way
	// the commands that RUN it do, and an omitted `backend` resolves to the
	// SANDBOXER_BACKEND default there. Judging it against nothing turned a
	// perfectly valid file into "the docker/podman container backend was
	// removed (got backend \"\")" — an error about a key the user never wrote.
	rt, err := config.ResolveRuntime(&p, config.LoadDefaults(), "", config.Overrides{})
	if err != nil {
		return err
	}
	if err := config.ValidateBackend(rt); err != nil {
		return err
	}
	return config.ValidateSession(rt)
}
