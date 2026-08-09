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
	var engineFlag, backendFlag, nixImage, configPath, llmAgentsRev, nixpkgsRev string
	var cache, keepBuilder, noRefresh bool
	var builderArgs []string
	cmd := &cobra.Command{
		Use:   "build [profile]",
		Short: "Build the toolbox image using only docker/podman (no host nix)",
		Long: `Build the sandboxer toolbox image without needing nix on this machine.

An ephemeral, public nixos/nix container (pulled by your engine) builds a minimal
image from a flake embedded in the sandboxer binary — agents come from the public
llm-agents.nix catalog — then the engine loads the result as ` + config.DefaultImage + `.

Auto-update is the default: each build first re-resolves the flake inputs
(nixpkgs, llm-agents — the agents) to their current remote heads and stamps the
resolved commits into the per-user pins cache, so a rebuild picks up new agent
releases. enter/exec only ever reuse the stamp — nothing re-resolves behind your
back; rebuild + recreate is how an update reaches a sandbox. --no-refresh builds
from the existing stamp (fast iteration on image.packages without moving the
agents). To hold an input still, pin it in the config: image.llmAgentsRev /
image.nixpkgsRev = a full 40-hex commit.

With a profile (a positional name or -f, resolved like enter/exec) the profile's
customized variant — tools packs, image.extraPkgs, an overlay, pinned input
revs — is built under its content-addressed var- tag instead. Without a profile
the stock default image is built.

--llm-agents-rev/--nixpkgs-rev override the input revs for this build only (over
the profile's values). A concrete bare-flag rev selects a var- tag: only a
profile pinning the same revs runs that image, so it pre-builds a variant but
does NOT update the stock image profile-less sandboxes use.

Clean by default: the builder container is removed, the nixos/nix image is dropped
again unless it was already present (or --keep-builder), and no persistent volume
is left behind. Use --cache to keep a nix-store volume for faster rebuilds.

Behind a proxy: the builder inherits the host's http_proxy/https_proxy/all_proxy/
no_proxy, rewriting a localhost proxy to the host gateway. For a proxy bound to
loopback only (a SOCKS5 tunnel client, say) give the builder the host's network
instead — then localhost really is localhost:

  sandboxer image build --cache --builder-arg=--network=host

If a substituter is reachable but crawling (the build stalls on repeated
"Timeout was reached ... less than 1 bytes/sec" warnings), cap the retries so nix
gives up on it and builds from source instead:

  sandboxer image build --cache \
    --builder-arg=--env --builder-arg='NIX_CONFIG=download-attempts = 1'`,
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
			// Stockness is decided BEFORE pin resolution: a tracking rev ("",
			// "latest") is the stock default, and PinSpec makes every rev
			// concrete — judged after it, nothing would ever be stock again.
			stock := spec.Empty()
			refresh := !noRefresh
			d := config.LoadDefaults()
			backendName, engine, err := imageBackend(backendFlag, engineFlag, prof, d)
			if err != nil {
				return err
			}
			// A microVM profile stores the built image where ITS backend reads it
			// — the tar store both runners build into, plus microsandbox's own
			// image store — not in a container engine's store, where enter would
			// never look. It shares nothing with the container path below. Pins
			// resolve on the HOST via git (no engine anywhere), so a cold cache
			// resolves exactly as on a container host.
			if config.IsMicrovmBackend(backendName) {
				if spec, err = toolbox.PinSpec(spec, refresh, cmd.ErrOrStderr()); err != nil {
					return err
				}
				image := d.Image
				if !stock {
					image = spec.Tag()
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "sandboxer: building toolbox image %q with host nix "+
					"(several minutes on first run)…\n", image)
				return backendBuildVMImage(engine, image, spec, cmd.ErrOrStderr())
			}
			// Probe the builder image BEFORE pin resolution: resolving a
			// tracking rev runs it (the engine auto-pulls), and clean-by-default
			// must still drop a builder image that pull brought in.
			builderImage := nixImage
			if builderImage == "" {
				builderImage = toolbox.NixImage
			}
			builderPulled := !backend.ImageExists(engine, builderImage)
			// Pin the tracking revs to concrete commits, stamping the pins cache
			// for later enter/exec. Refresh (the default) re-resolves the remote
			// heads first (host git), so a rebuild picks up new agent releases;
			// --no-refresh builds from the existing stamp.
			spec, err = toolbox.PinSpec(spec, refresh, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			image := d.Image
			if !stock {
				image = spec.Tag()
			}
			if err := toolboxBuild(toolbox.BuildOpts{
				Engine:        engine,
				Image:         image,
				NixImage:      nixImage,
				Cache:         cache,
				KeepBuilder:   keepBuilder,
				ExtraArgs:     builderArgs,
				Spec:          spec,
				BuilderPulled: builderPulled,
				Stdout:        cmd.OutOrStdout(),
				Stderr:        cmd.ErrOrStderr(),
			}); err != nil {
				return err
			}
			// Bare concrete rev flags (no profile) build a variant nothing
			// selects by default — say so instead of letting the user believe
			// the stock image their sandboxes run was just updated.
			if prof == nil && !stock {
				fmt.Fprintf(cmd.ErrOrStderr(), "sandboxer: note: built variant %s — the stock %s was "+
					"not rebuilt; only a profile pinning the same revs uses this image\n", image, d.Image)
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&backendFlag, "backend", "", "backend: docker | podman | microvm | microsandbox (default: the profile's, else docker)")
	fl.StringVar(&engineFlag, "engine", "", "container engine: docker | podman (default: auto-detect); ignored for a microVM backend")
	fl.StringVarP(&configPath, "config", "f", "", "profile file (default: the project sandboxer.nix; pick a profiles section by name)")
	fl.StringVar(&nixImage, "nix-image", "", "builder image (default: pinned "+toolbox.NixImage+")")
	fl.BoolVar(&cache, "cache", false, "keep a persistent nix-store volume for faster rebuilds")
	fl.BoolVar(&keepBuilder, "keep-builder", false, "keep the nixos/nix builder image afterward")
	fl.BoolVar(&noRefresh, "no-refresh", false, "build from the stamped input revs instead of re-resolving the remote heads")
	fl.StringVar(&llmAgentsRev, "llm-agents-rev", "", "llm-agents input rev for this build: latest or a full 40-hex commit (overrides the profile)")
	fl.StringVar(&nixpkgsRev, "nixpkgs-rev", "", "nixpkgs input rev for this build: latest or a full 40-hex commit (overrides the profile)")
	fl.StringArrayVar(&builderArgs, "builder-arg", nil, "extra engine `run` flag for the builder (repeatable)")
	return cmd
}

// toolboxBuild is the image-build seam, overridable in tests so the command's
// flag/pin orchestration can be exercised without a real engine — the
// backendRun seam's sibling.
var toolboxBuild = toolbox.BuildImage

// backendBuildVMImage is the microVM image-build seam (built in a microVM,
// stored in the tar store), overridable in tests.
var backendBuildVMImage = backend.BuildVMImage

// imageBackend resolves where `image build` / `image rm` act: the isolation
// backend (flag > profile > default) and, for a container backend, the engine
// binary (--engine wins, else the backend hint). For a microVM backend the
// engine is its runner's identity — smolvm's or microsandbox's — and
// ResolveEngine errors if that runner is absent. This is the fix for image
// build/rm having only known --engine: without it an image built for a
// `backend = "microvm"` profile went to a container engine's store, which the
// microVM backends never read.
func imageBackend(backendFlag, engineFlag string, prof *config.Profile, d config.Defaults) (backendName, engine string, err error) {
	backendName = backendFlag
	if backendName == "" && prof != nil {
		backendName = prof.Backend
	}
	if backendName == "" {
		backendName = d.Backend
	}
	if config.IsMicrovmBackend(backendName) {
		engine, err = backend.ResolveEngine(backendName, d)
		return backendName, engine, err
	}
	engine, err = backend.ResolveEngine(firstNonEmpty(engineFlag, backendName), d)
	return backendName, engine, err
}

// buildImageProfile resolves image build's optional profile — a profile file
// (-f) or a section of the project's multi-profile config — through the same
// chain enter/exec use. No argument and no -f mean no
// profile: the stock default image is built, deliberately NOT auto-discovering
// sandboxer.nix so a bare `sandboxer image build` keeps today's behavior.
func buildImageProfile(configPath, pos string) (*config.Profile, error) {
	if configPath == "" && pos == "" {
		return nil, nil
	}
	file, sel, err := resolveProfileFile(configPath, getwd(), pos)
	if err != nil {
		return nil, err
	}
	if file == "" {
		return nil, fmt.Errorf("no profile %q (a *.nix file, or a profiles section of %s)",
			pos, config.ConfigPath())
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
