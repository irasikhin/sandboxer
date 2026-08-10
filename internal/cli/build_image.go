package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/backend"
	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/toolbox"
)

// newImageBuildCmd builds the toolbox image with host nix and stores it where
// the profile's microVM backend reads it — the shared tar store both runners
// boot from, plus microsandbox's own image store. It is the `build` verb of
// the `image` command group (see image.go).
func newImageBuildCmd() *cobra.Command {
	var backendFlag, configPath, llmAgentsRev, nixpkgsRev string
	var noRefresh bool
	cmd := &cobra.Command{
		Use:   "build [profile]",
		Short: "Build the toolbox image with host nix into the microVM store",
		Long: `Build the sandboxer toolbox image with host nix (nix is already a hard
requirement of the CLI) and place it in the microVM image store, where the
backend boots it from. With backend = "microsandbox" the tar is additionally
imported into msb's own image store.

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
does NOT update the stock image profile-less sandboxes use.`,
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
			// or a full 40-hex commit), checked before any build work.
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
			d := config.LoadDefaults()
			engine, err := imageBackend(backendFlag, prof, d)
			if err != nil {
				return err
			}
			// Pins resolve on the HOST via git, so a cold cache resolves the
			// same way everywhere; refresh (the default) re-resolves the remote
			// heads first so a rebuild picks up new agent releases.
			if spec, err = toolbox.PinSpec(spec, !noRefresh, cmd.ErrOrStderr()); err != nil {
				return err
			}
			image := d.Image
			if !stock {
				image = spec.Tag()
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "sandboxer: building toolbox image %q with host nix "+
				"(several minutes on first run)…\n", image)
			if err := backendBuildVMImage(engine, image, spec, cmd.ErrOrStderr()); err != nil {
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
	fl.StringVar(&backendFlag, "backend", "", "backend: microsandbox (default: the profile's, else microsandbox)")
	fl.StringVarP(&configPath, "config", "f", "", "profile file (default: the project sandboxer.nix; pick a profiles section by name)")
	fl.BoolVar(&noRefresh, "no-refresh", false, "build from the stamped input revs instead of re-resolving the remote heads")
	fl.StringVar(&llmAgentsRev, "llm-agents-rev", "", "llm-agents input rev for this build: latest or a full 40-hex commit (overrides the profile)")
	fl.StringVar(&nixpkgsRev, "nixpkgs-rev", "", "nixpkgs input rev for this build: latest or a full 40-hex commit (overrides the profile)")
	return cmd
}

// backendBuildVMImage is the microVM image-build seam (built with host nix,
// stored in the tar store), overridable in tests.
var backendBuildVMImage = backend.BuildVMImage

// imageBackend resolves where `image build` / `image rm` act: the isolation
// backend (flag > profile > default) resolved to its runner's engine identity.
// ResolveEngine errors when the runner is absent, and when the backend is a
// removed name (container-era, or the retired smolvm "microvm").
func imageBackend(backendFlag string, prof *config.Profile, d config.Defaults) (string, error) {
	backendName := backendFlag
	if backendName == "" && prof != nil {
		backendName = prof.Backend
	}
	if backendName == "" {
		backendName = d.Backend
	}
	return backend.ResolveEngine(backendName, d)
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
