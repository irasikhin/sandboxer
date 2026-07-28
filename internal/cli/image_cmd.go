package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/backend"
	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/toolbox"
)

func init() { register(newImageCmd) }

// newImageCmd groups the toolbox-image verbs — building it, editing its nix
// hook, and removing the built image — under `sandboxer image`. This is the
// "form the image" half of the workflow: image.nix + config drive what the
// agent's container is built from.
func newImageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Build, edit and remove the toolbox image",
		Long: `Manage the toolbox image the sandbox runs in.

  sandboxer image build   build it (in a builder container)
  sandboxer image rm      remove a built image

Customize it from the config: image.packages/files/env, or a nixpkgs overlay
file via image.overlay (edit with 'sandboxer config edit').`,
	}
	cmd.AddCommand(newImageBuildCmd(), newImageRmCmd())
	return cmd
}

// backendRemoveImage is the image-removal seam, overridable in tests so
// `image rm` can be exercised without a real engine.
var backendRemoveImage = backend.RemoveImage

// newImageRmCmd removes a built toolbox image: the stock default image, or —
// with a profile — that profile's content-addressed variant tag. Idempotent:
// an already-absent image is success.
func newImageRmCmd() *cobra.Command {
	var engineFlag, backendFlag, configPath string
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
					image = spec.Tag()
				}
			}
			// Resolve the backend BEFORE removing: a microvm image lives in the
			// tar store, not a container engine, and RemoveImage dispatches on the
			// resolved engine ("smolvm" identity) to reach the right one.
			_, engine, err := imageBackend(backendFlag, engineFlag, prof, d)
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
	fl.StringVar(&backendFlag, "backend", "", "backend: docker | podman | microvm | microsandbox (default: the profile's, else docker)")
	fl.StringVar(&engineFlag, "engine", "", "container engine: docker | podman (default: auto-detect); ignored for a microVM backend")
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
