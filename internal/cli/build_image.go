package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/backend"
	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/toolbox"
)

// newImageBuildCmd builds the toolbox image with only docker/podman on the host
// (no host nix): an ephemeral, public nixos/nix container realizes the image
// from a flake embedded in the binary, then the engine loads it. It is the
// `build` verb of the `image` command group (see image.go).
func newImageBuildCmd() *cobra.Command {
	var engineFlag, nixImage, configPath, llmAgentsRev, nixpkgsRev string
	var cache, keepBuilder, refresh bool
	var builderArgs []string
	cmd := &cobra.Command{
		Use:   "build [profile]",
		Short: "Build the toolbox image using only docker/podman (no host nix)",
		Long: `Build the sandboxer toolbox image without needing nix on this machine.

An ephemeral, public nixos/nix container (pulled by your engine) builds a minimal
image from a flake embedded in the sandboxer binary — agents come from the public
llm-agents.nix catalog and the sandboxer binary is injected by copy — then the
engine loads the result as ` + config.DefaultImage + `.

With a profile (a positional name or -f, resolved like enter/exec) the profile's
customized variant — tools packs, image.extraPkgs, an image.nix hook, pinned
input revs — is built under its content-addressed var- tag instead. Without a
profile and without rev flags the stock default image is built, as before.

--llm-agents-rev/--nixpkgs-rev override the input revs for this build only (over
the profile's values). Any rev override — flag or profile — selects a var- tag
too: only a profile pinning the same revs runs that image, so the bare-flag form
pre-resolves the pins and pre-builds a variant but does NOT update the stock
image profile-less sandboxes use. A "latest" rev is resolved to the remote head
once and stamped into the per-user pins cache; later enter/exec reuse the stamp,
and only --refresh re-resolves it.

Clean by default: the builder container is removed, the nixos/nix image is dropped
again unless it was already present (or --keep-builder), and no persistent volume
is left behind. Use --cache to keep a nix-store volume for faster rebuilds.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prof, err := buildImageProfile(configPath, posArg(args))
			if err != nil {
				return err
			}
			spec, err := toolbox.ResolveSpec(prof)
			if err != nil {
				return err
			}
			// One-shot flag overrides land on top of the profile's revs; the
			// merged values obey the same rules as the profile fields (latest
			// or a full 40-hex commit), checked before any engine work.
			if llmAgentsRev != "" {
				spec.LLMAgentsRev = llmAgentsRev
			}
			if nixpkgsRev != "" {
				spec.NixpkgsRev = nixpkgsRev
			}
			if err := config.ValidateImageSpec(config.ImageSpec{
				LLMAgentsRev: spec.LLMAgentsRev, NixpkgsRev: spec.NixpkgsRev,
			}); err != nil {
				return err
			}
			d := config.LoadDefaults()
			engine, err := backend.ResolveEngine(engineFlag, d)
			if err != nil {
				return err
			}
			// Probe the builder image BEFORE pin resolution: resolving a
			// "latest" rev runs it (the engine auto-pulls), and clean-by-default
			// must still drop a builder image that pull brought in.
			builderImage := nixImage
			if builderImage == "" {
				builderImage = toolbox.NixImage
			}
			builderPulled := !backend.ImageExists(engine, builderImage)
			// Pin any "latest" rev to a concrete commit, stamping the pins
			// cache for later enter/exec; --refresh forces a re-resolve so the
			// stamp (and every tag derived from it) moves forward.
			spec, err = toolbox.PinSpec(spec, engine, nixImage, refresh, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			image := d.Image
			if !spec.Empty() {
				image = spec.Tag()
			}
			if err := toolboxBuild(toolbox.BuildOpts{
				Engine:        engine,
				Image:         image,
				NixImage:      nixImage,
				Cache:         cache,
				KeepBuilder:   keepBuilder,
				Refresh:       refresh,
				ExtraArgs:     builderArgs,
				Spec:          spec,
				BuilderPulled: builderPulled,
				Stdout:        cmd.OutOrStdout(),
				Stderr:        cmd.ErrOrStderr(),
			}); err != nil {
				return err
			}
			// Bare rev flags (no profile) build a variant nothing selects by
			// default — say so instead of letting the user believe the stock
			// image their sandboxes run was just updated.
			if prof == nil && !spec.Empty() {
				fmt.Fprintf(cmd.ErrOrStderr(), "sandboxer: note: built variant %s — the stock %s was "+
					"not rebuilt; only a profile pinning the same revs uses this image\n", image, d.Image)
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&engineFlag, "engine", "", "container engine: docker | podman (default: auto-detect)")
	fl.StringVarP(&configPath, "config", "f", "", "profile: a file, a directory of profiles, or a named profile (store: ~/.config/sandboxer/profiles)")
	fl.StringVar(&nixImage, "nix-image", "", "builder image (default: pinned "+toolbox.NixImage+")")
	fl.BoolVar(&cache, "cache", false, "keep a persistent nix-store volume for faster rebuilds")
	fl.BoolVar(&keepBuilder, "keep-builder", false, "keep the nixos/nix builder image afterward")
	fl.BoolVar(&refresh, "refresh", false, "re-fetch flake inputs and re-resolve any latest rev pin")
	fl.StringVar(&llmAgentsRev, "llm-agents-rev", "", "llm-agents input rev for this build: latest or a full 40-hex commit (overrides the profile)")
	fl.StringVar(&nixpkgsRev, "nixpkgs-rev", "", "nixpkgs input rev for this build: latest or a full 40-hex commit (overrides the profile)")
	fl.StringArrayVar(&builderArgs, "builder-arg", nil, "extra engine `run` flag for the builder (repeatable)")
	return cmd
}

// toolboxBuild is the image-build seam, overridable in tests so the command's
// flag/pin orchestration can be exercised without a real engine — the
// backendRun seam's sibling.
var toolboxBuild = toolbox.BuildImage

// buildImageProfile resolves image build's optional profile — a named profile,
// a profile file/directory (-f), or a section of a multi-profile file —
// through the same chain enter/exec use. No argument and no -f mean no
// profile: the stock default image is built, deliberately NOT auto-discovering
// .sandboxer/config.yaml so a bare `sandboxer image build` keeps today's behavior.
func buildImageProfile(configPath, pos string) (*config.Profile, error) {
	if configPath == "" && pos == "" {
		return nil, nil
	}
	file, sel, err := resolveProfileFile(configPath, getwd(), pos)
	if err != nil {
		return nil, err
	}
	if file == "" {
		return nil, fmt.Errorf("no profile %q (a *.yaml file, a named profile in %s, or a section of %s)",
			pos, config.ProfilesDir(), config.ConfigPath())
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
