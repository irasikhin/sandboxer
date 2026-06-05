package cli

import (
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
	var engineFlag, nixImage string
	var cache, keepBuilder, refresh bool
	var builderArgs []string
	cmd := &cobra.Command{
		Use:   "build-image",
		Short: "Build the toolbox image using only docker/podman (no host nix)",
		Long: `Build the sandboxer toolbox image without needing nix on this machine.

An ephemeral, public nixos/nix container (pulled by your engine) builds a minimal
image from a flake embedded in the sandboxer binary — agents come from the public
llm-agents.nix catalog and the sandboxer binary is injected by copy — then the
engine loads the result as ` + config.DefaultImage + `.

Clean by default: the builder container is removed, the nixos/nix image is dropped
again unless it was already present (or --keep-builder), and no persistent volume
is left behind. Use --cache to keep a nix-store volume for faster rebuilds.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d := config.LoadDefaults()
			engine, err := backend.ResolveEngine(engineFlag, d)
			if err != nil {
				return err
			}
			return toolbox.BuildImage(toolbox.BuildOpts{
				Engine:      engine,
				Image:       d.Image,
				NixImage:    nixImage,
				Cache:       cache,
				KeepBuilder: keepBuilder,
				Refresh:     refresh,
				ExtraArgs:   builderArgs,
				Stdout:      cmd.OutOrStdout(),
				Stderr:      cmd.ErrOrStderr(),
			})
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&engineFlag, "engine", "", "container engine: podman | docker (default: auto-detect)")
	fl.StringVar(&nixImage, "nix-image", "", "builder image (default: pinned "+toolbox.NixImage+")")
	fl.BoolVar(&cache, "cache", false, "keep a persistent nix-store volume for faster rebuilds")
	fl.BoolVar(&keepBuilder, "keep-builder", false, "keep the nixos/nix builder image afterward")
	fl.BoolVar(&refresh, "refresh", false, "re-fetch flake inputs (latest agents)")
	fl.StringArrayVar(&builderArgs, "builder-arg", nil, "extra engine `run` flag for the builder (repeatable)")
	return cmd
}
