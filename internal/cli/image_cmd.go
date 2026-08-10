package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/backend"
	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/toolbox"
)

func init() { register(newImageCmd) }

// newImageCmd groups the toolbox-image verbs — pulling the prebuilt one,
// building locally and removing — under `sandboxer image`. This is the "form
// the image" half of the workflow: the config's image section drives what the
// agent's machine is built from.
func newImageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Pull, build and remove the toolbox image",
		Long: `Manage the toolbox image the sandbox runs in. The stock image comes
PREBUILT (` + config.DefaultImage + `), pulled by msb on first use.

  sandboxer image pull    pull it, or refresh an already-cached one
  sandboxer image build   build it locally with host nix (customized
                          profiles, offline hosts)
  sandboxer image rm      remove a pulled/built image

Customize it from the config: image.packages/files/env, or a nixpkgs overlay
file via image.overlay (edit with 'sandboxer config edit').`,
	}
	cmd.AddCommand(newImagePullCmd(), newImageBuildCmd(), newImageRmCmd())
	return cmd
}

// backendPullImage is the image-pull seam, overridable in tests.
var backendPullImage = backend.PullImage

// newImagePullCmd fetches the prebuilt toolbox image into msb's store — the
// refresh story for a moved `latest`: a create only pulls a ref MISSING from
// the store, so the nightly-republished default never reaches an
// already-cached host without this command. The image resolves exactly like
// build/rm (SANDBOXER_IMAGE, then the default); a profile with image
// customization runs a locally-built variant that is never published, so it
// is refused with the build hint instead of pulling something that cannot
// exist.
func newImagePullCmd() *cobra.Command {
	var backendFlag, configPath string
	cmd := &cobra.Command{
		Use:   "pull [profile]",
		Short: "Pull (or refresh) the prebuilt toolbox image",
		Long: `Pull the prebuilt toolbox image into microsandbox's image store (msb pull),
host-side — the pull honors the shell's HTTP(S)_PROXY. The default image is
republished nightly (the agents move with their releases) and per release tag,
while a create only pulls a ref that is MISSING from the store — refreshing an
already-cached ` + "`latest`" + ` is what this command is for.

A live session picks the fresh image up on its next enter: it reads as stale
and is recreated (the tmux layout is saved and restored). Profiles that
customize the image (tools, image.packages/overlay) run a locally-built
variant that is never published — build those with 'sandboxer image build'.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := config.LoadDefaults()
			prof, err := buildImageProfile(configPath, posArg(args))
			if err != nil {
				return err
			}
			if prof != nil {
				spec, err := toolbox.ResolveSpec(prof)
				if err != nil {
					return err
				}
				if !spec.Empty() {
					return fmt.Errorf("this profile customizes the image — its variant is built " +
						"locally, never published; build it with: sandboxer image build")
				}
			}
			engine, err := imageBackend(backendFlag, prof, d)
			if err != nil {
				return err
			}
			if err := backendPullImage(engine, d.Image, cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "pulled image: %s\n", d.Image)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&backendFlag, "backend", "", "backend: microsandbox (default: the profile's, else microsandbox)")
	fl.StringVarP(&configPath, "config", "f", "", "profile file (checked for image customization, which cannot be pulled)")
	return cmd
}

// backendRemoveImage is the image-removal seam, overridable in tests so
// `image rm` can be exercised without a real engine.
var backendRemoveImage = backend.RemoveImage

// newImageRmCmd removes a built toolbox image: the stock default image, or —
// with a profile — that profile's content-addressed variant tag. Idempotent:
// an already-absent image is success.
func newImageRmCmd() *cobra.Command {
	var backendFlag, configPath string
	cmd := &cobra.Command{
		Use:   "rm [profile]",
		Short: "Remove a built toolbox image (stock, or a profile's variant)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := config.LoadDefaults()
			image := d.Image
			prof, err := buildImageProfile(configPath, posArg(args))
			if err != nil {
				return err
			}
			if prof != nil {
				spec, err := toolbox.ResolveSpec(prof)
				if err != nil {
					return err
				}
				if !spec.Empty() {
					// Resolve tracking revs so the variant tag matches what would
					// be built (host git on a cold cache — no engine, no resolver
					// container). A cold cache simply resolves the latest revs,
					// which is exactly how the tag was computed at build time;
					// there is no fail-closed "nothing was ever built" wall
					// anymore.
					if spec, err = toolbox.PinSpec(spec, false, io.Discard); err != nil {
						return err
					}
					image = spec.Tag()
				}
			}
			// Resolve the backend BEFORE removing: it errors early on a
			// removed backend name or a missing runner, and RemoveImage drops
			// both the msb-cached copy and the build tar it was imported from.
			engine, err := imageBackend(backendFlag, prof, d)
			if err != nil {
				return err
			}
			if err := backendRemoveImage(engine, image); err != nil {
				return fmt.Errorf("remove image %s: %w", image, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed image: %s\n", image)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&backendFlag, "backend", "", "backend: microsandbox (default: the profile's, else microsandbox)")
	fl.StringVarP(&configPath, "config", "f", "", "profile whose image variant to remove")
	return cmd
}

// openInEditor launches $VISUAL/$EDITOR on path (falling back to vi), wiring the
// child process to the command's stdio so an interactive editor works. Used by
// `config edit`.
func openInEditor(cmd *cobra.Command, path string) error {
	editor := firstNonEmpty(os.Getenv("VISUAL"), os.Getenv("EDITOR"))
	if editor == "" {
		editor = "vi"
	}
	ed := exec.Command(editor, path) //nolint:gosec // the editor is the user's own configured program
	ed.Stdin, ed.Stdout, ed.Stderr = cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()
	if err := ed.Run(); err != nil {
		return fmt.Errorf("editor %q on %s: %w", editor, filepath.Base(path), err)
	}
	return nil
}
