package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/config"
)

func init() { register(newInitCmd) }

// imageNixFileName is the starter image hook `sandboxer init` writes beside the
// profile; the scaffolded image.nix points at it (see starterImageSection).
const imageNixFileName = "sandbox-image.nix"

func newInitCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init [name]",
		Short: "Write a starter " + config.ConfigFileName + " (and " + imageNixFileName + ") in the current directory",
		Long: `Scaffold a commented ` + config.ConfigFileName + ` in the current directory so you
have a concrete config to edit instead of relying on the silent defaults. It is
auto-discovered by create/enter/exec/run here (no -f needed). A starter
` + imageNixFileName + ` image hook is written alongside, wired in via the
profile's image: section (delete that block for the stock toolbox image).`,
		Example: `  sandboxer init            # name defaults to the directory
  sandboxer init web        # set the profile name
  sandboxer init --force    # overwrite an existing ` + config.ConfigFileName,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := config.ConfigFileName
			if fileExists(path) && !force {
				return fmt.Errorf("%s already exists (use --force to overwrite)", path)
			}
			if fileExists(imageNixFileName) && !force {
				return fmt.Errorf("%s already exists (use --force to overwrite)", imageNixFileName)
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
			if err := os.WriteFile(imageNixFileName, []byte(starterImageNix), 0o644); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "wrote %s + %s (name=%s backend=%s agent=%s)\n", path, imageNixFileName, name, d.Backend, d.Agent)
			fmt.Fprintln(out, "edit them, then: sandboxer create")
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing "+config.ConfigFileName+"/"+imageNixFileName)
	return cmd
}

// maybeAutoScaffold writes a default .sandboxer.yaml into the project root and
// points this run at it when the user has no config at all — so create/enter in
// a fresh project land on a concrete, announced profile instead of silent
// defaults. It is a no-op (current behaviour) when an explicit -f is given, a
// project config already exists, we're inside the container, or the user opts
// out with SANDBOXER_NO_SCAFFOLD=1.
//
// Like the explicit `init`, it scaffolds the active image: section and a
// sandbox-image.nix hook beside the config, so the custom image works on the
// auto-scaffold path too (enter/exec/run build the variant on first use, with a
// one-time notice). An existing sandbox-image.nix is left untouched.
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
	nixPath := filepath.Join(root, imageNixFileName)
	if !fileExists(nixPath) {
		if err := os.WriteFile(nixPath, []byte(starterImageNix), 0o644); err != nil {
			return err
		}
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"sandboxer: no %s — scaffolded a default (name=%s; edit it, or set SANDBOXER_NO_SCAFFOLD=1 to skip)\n", config.ConfigFileName, name)
	f.config = path
	return nil
}

// starterProfile renders a commented .sandboxer.yaml seeded with the effective
// defaults (so it reflects the user's environment) and the common knobs left as
// hints to fill in, plus an active image: section wired to the sandbox-image.nix
// hook both init and auto-scaffold write alongside.
func starterProfile(name string, d config.Defaults) string {
	domains := strings.ReplaceAll(d.Domains, ",", ", ")
	profile := fmt.Sprintf(`# sandboxer profile — edit to taste. Auto-discovered when you run sandboxer
# in this directory (no -f needed). Full reference: examples/ in the repo.

# Sandbox name (slug); drives .sandboxer/<name>/.
name: %s

# Isolation backend: docker | podman.
backend: %s

# Coding agent — see: sandboxer agents.
agent: %s

# Egress allowlist: the ONLY domains the sandbox may reach (everything else is
# blocked). Seeded with the effective defaults (SANDBOXER_DOMAINS or the built-in
# set — AI APIs + the common package registries: npm, PyPI, Maven, Gradle,
# crates, Go, RubyGems, GitHub). Trim to what your task needs.
network:
  allowedDomains: [%s]

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
`, name, d.Backend, d.Agent, domains)
	profile += starterImageSection
	return profile
}

// starterImageSection is the active image: block appended by `sandboxer init`.
// It points at the sandbox-image.nix hook written alongside; the spec is
// non-empty (a nix hook is set), so the first create builds a content-addressed
// toolbox variant, cached thereafter — the stock image is untouched.
const starterImageSection = `
# Custom toolbox image (optional). Sandboxes in this profile run a
# content-addressed variant built on first 'create' (cached after; the stock
# sandboxer-toolbox:latest is untouched). Delete this block for the stock image.
image:
  extraPkgs: []            # extra nixpkgs attrs baked in (dotted paths allowed)
  nix: ` + imageNixFileName + `   # { packages, files, env, overlay } hook — see that file
  # llmAgentsRev: latest   # flake-input pin overrides; empty keeps embedded pins
  # nixpkgsRev: <full 40-char hex commit>
`

// starterImageNix is the annotated, inert image hook `sandboxer init` writes.
// Every example is commented, so it evaluates to { } (the variant is content-
// equivalent to the stock image) until the user uncomments something.
const starterImageNix = `# sandbox-image.nix — the image hook this profile's image.nix points at,
# imported by the embedded toolbox flake during 'sandboxer build-image' (or the
# auto-build on first enter). A function over { pkgs } returning any of FOUR
# keys: packages, files, env, overlay. The contract is fail-closed — an unknown
# key aborts the build, so a typo never silently drops a customization.
#
# Two-phase evaluation: the function is first called with BASE nixpkgs and only
# 'overlay' is read; nixpkgs is then re-imported with the overlay applied, and
# the function is called again with that final 'pkgs' for packages/files/env.
# Everything below is commented — uncomment what you need.
{ pkgs }:
{
  # nixpkgs overlay (phase 1): add or override packages; everything in phase 2
  # sees the result. (An overlay cannot reference its own result.)
  # overlay = final: prev: {
  #   hello-sandboxer = prev.writeShellScriptBin "hello-sandboxer" ''
  #     echo "hello from a custom toolbox image"
  #   '';
  # };

  # Extra store paths baked into the image — 'pkgs' has the overlay applied.
  # packages = [
  #   pkgs.httpie
  # ];

  # Text files at absolute paths inside the image. /etc/sandboxer/rc.d/*.sh are
  # the shell's plugin drop-ins, sourced by every interactive shell.
  # files = {
  #   "/etc/sandboxer/rc.d/10-custom.sh" = ''
  #     alias hi=hello-sandboxer
  #   '';
  # };

  # Appended to the image's OCI env; a user variable overrides a same-named
  # default (last occurrence wins).
  # env = {
  #   SANDBOX_FLAVOR = "custom";
  # };
}
`
