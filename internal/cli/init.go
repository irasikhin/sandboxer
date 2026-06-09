package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/config"
)

func init() { register(newInitCmd) }

func newInitCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init [name]",
		Short: "Write a starter " + config.ConfigFileName + " in the current directory",
		Long: `Scaffold a commented ` + config.ConfigFileName + ` in the current directory so you
have a concrete config to edit instead of relying on the silent defaults. It is
auto-discovered by create/enter/exec/run here (no -f needed).`,
		Example: `  sandboxer init            # name defaults to the directory
  sandboxer init web        # set the profile name
  sandboxer init --force    # overwrite an existing ` + config.ConfigFileName,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := config.ConfigFileName
			if fileExists(path) && !force {
				return fmt.Errorf("%s already exists (use --force to overwrite)", path)
			}
			name := config.Sanitize(posArg(args))
			if name == "" {
				if wd, err := os.Getwd(); err == nil {
					name = config.Sanitize(filepath.Base(wd))
				}
			}
			if name == "" {
				name = "feat"
			}
			d := config.LoadDefaults()
			if err := os.WriteFile(path, []byte(starterProfile(name, d)), 0o644); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "wrote %s (name=%s backend=%s agent=%s)\n", path, name, d.Backend, d.Agent)
			fmt.Fprintln(out, "edit it, then: sandboxer create")
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing "+config.ConfigFileName)
	return cmd
}

// maybeAutoScaffold writes a default .sandboxer.yaml into the project root and
// points this run at it when the user has no config at all — so create/enter in
// a fresh project land on a concrete, announced profile instead of silent
// defaults. It is a no-op (current behaviour) when an explicit -f is given, a
// project config already exists, we're inside the container, or the user opts
// out with SANDBOXER_NO_SCAFFOLD=1.
func maybeAutoScaffold(cmd *cobra.Command, f *commonFlags, pos string) error {
	if f.config != "" || inContainer() || os.Getenv("SANDBOXER_NO_SCAFFOLD") == "1" {
		return nil
	}
	root := firstNonEmpty(f.src, getwd())
	path := filepath.Join(root, config.ConfigFileName)
	if fileExists(path) {
		return nil // a project config already exists; leave resolution as-is
	}
	name := config.Sanitize(pos)
	if name == "" {
		name = config.Sanitize(filepath.Base(root))
	}
	if name == "" {
		name = "feat"
	}
	if err := os.WriteFile(path, []byte(starterProfile(name, config.LoadDefaults())), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"sandboxer: no %s — scaffolded a default (name=%s; edit it, or set SANDBOXER_NO_SCAFFOLD=1 to skip)\n", config.ConfigFileName, name)
	f.config = path
	return nil
}

// starterProfile renders a commented .sandboxer.yaml seeded with the effective
// defaults (so it reflects the user's environment) and the common knobs left as
// hints to fill in.
func starterProfile(name string, d config.Defaults) string {
	return fmt.Sprintf(`# sandboxer profile — edit to taste. Auto-discovered when you run sandboxer
# in this directory (no -f needed). Full reference: examples/ in the repo.

# Sandbox name (slug); drives .sandboxer/<name>/.
name: %s

# Isolation backend: native (claude only) | podman | docker.
backend: %s

# Coding agent — see: sandboxer agents.
agent: %s

# Egress allowlist: the ONLY domains the sandbox may reach (everything else is
# blocked). Trim to what your task needs.
network:
  allowedDomains: [api.anthropic.com, github.com, registry.npmjs.org]

# Sandbox content. Nothing is copied unless listed here: each dep is located by
# path suffix under roots, copied INTO the sandbox, and pushed back with
# 'sandboxer push'. Uncomment and adjust:
# roots: [.]
# deps:
#   - src/lib

# Extra bind mounts / env for the container backend (optional):
# extraMounts:
#   - { source: /data/cache, target: /data/cache, mode: rw }
# env:
#   NODE_ENV: development
`, name, d.Backend, d.Agent)
}
