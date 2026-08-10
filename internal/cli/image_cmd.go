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

// newImageCmd groups the toolbox-image verbs — building it and removing the
// built image — under `sandboxer image`. This is the "form the image" half of
// the workflow: the config's image section drives what the agent's machine is
// built from.
func newImageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Build, edit and remove the toolbox image",
		Long: `Manage the toolbox image the sandbox runs in.

  sandboxer image build   build it (with host nix)
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
			// Resolve the backend BEFORE removing: a smolvm image lives in the
			// shared tar store alone, while a microsandbox one is also cached in
			// msb's own image store — RemoveImage dispatches on the resolved
			// engine identity to reach the right one.
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
	fl.StringVar(&backendFlag, "backend", "", "backend: microsandbox | microvm (default: the profile's, else microsandbox)")
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
