package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/backend"
	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/toolbox"
)

func init() { register(newBuildImageCmd) }

// newBuildImageCmd builds the toolbox image with only docker/podman on the host
// (no host nix): an ephemeral, public nixos/nix container realizes the image
// from a flake embedded in the binary, then the engine loads it.
func newBuildImageCmd() *cobra.Command {
	var engineFlag, nixImage, configPath string
	var cache, keepBuilder, refresh bool
	var builderArgs []string
	cmd := &cobra.Command{
		Use:   "build-image [profile]",
		Short: "Build the toolbox image using only docker/podman (no host nix)",
		Long: `Build the sandboxer toolbox image without needing nix on this machine.

An ephemeral, public nixos/nix container (pulled by your engine) builds a minimal
image from a flake embedded in the sandboxer binary — agents come from the public
llm-agents.nix catalog and the sandboxer binary is injected by copy — then the
engine loads the result as ` + config.DefaultImage + `.

With a profile (a positional name or -f, resolved like enter/exec) the profile's
customized variant — tools packs, image.extraPkgs, an image.nix hook, pinned
input revs — is built under its content-addressed var- tag instead. Without one
the stock default image is built, as before.

Clean by default: the builder container is removed, the nixos/nix image is dropped
again unless it was already present (or --keep-builder), and no persistent volume
is left behind. Use --cache to keep a nix-store volume for faster rebuilds.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := config.LoadDefaults()
			engine, err := backend.ResolveEngine(engineFlag, d)
			if err != nil {
				return err
			}
			prof, err := buildImageProfile(configPath, posArg(args))
			if err != nil {
				return err
			}
			image, spec, err := resolveImage(prof)
			if err != nil {
				return err
			}
			return toolbox.BuildImage(toolbox.BuildOpts{
				Engine:      engine,
				Image:       image,
				NixImage:    nixImage,
				Cache:       cache,
				KeepBuilder: keepBuilder,
				Refresh:     refresh,
				ExtraArgs:   builderArgs,
				Spec:        spec,
				Stdout:      cmd.OutOrStdout(),
				Stderr:      cmd.ErrOrStderr(),
			})
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&engineFlag, "engine", "", "container engine: podman | docker (default: auto-detect)")
	fl.StringVarP(&configPath, "config", "f", "", "profile: a file, a directory of profiles, or a named profile (store: ~/.config/sandboxer/profiles)")
	fl.StringVar(&nixImage, "nix-image", "", "builder image (default: pinned "+toolbox.NixImage+")")
	fl.BoolVar(&cache, "cache", false, "keep a persistent nix-store volume for faster rebuilds")
	fl.BoolVar(&keepBuilder, "keep-builder", false, "keep the nixos/nix builder image afterward")
	fl.BoolVar(&refresh, "refresh", false, "re-fetch flake inputs (latest agents)")
	fl.StringArrayVar(&builderArgs, "builder-arg", nil, "extra engine `run` flag for the builder (repeatable)")
	return cmd
}

// buildImageProfile resolves build-image's optional profile — a named profile,
// a profile file/directory (-f), or a section of a multi-profile file —
// through the same chain enter/exec use. No argument and no -f mean no
// profile: the stock default image is built, deliberately NOT auto-discovering
// ./.sandboxer.yaml so a bare `sandboxer build-image` keeps today's behavior.
func buildImageProfile(configPath, pos string) (*config.Profile, error) {
	if configPath == "" && pos == "" {
		return nil, nil
	}
	file, sel, err := resolveProfileFile(configPath, pos)
	if err != nil {
		return nil, err
	}
	if file == "" {
		return nil, fmt.Errorf("no profile %q (a *.yaml file, a named profile in %s, or a section of %s)",
			pos, config.ProfilesDir(), config.ConfigFileName)
	}
	doc, err := config.LoadDocument(file)
	if err != nil {
		return nil, err
	}
	if doc.Multi() {
		return doc.Select(sel)
	}
	return doc.Select("")
}
