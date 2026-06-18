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

  sandboxer image build   build it (docker/podman, no host nix needed)
  sandboxer image edit    edit the .sandboxer/image.nix customization hook
  sandboxer image rm      remove a built image`,
	}
	cmd.AddCommand(newImageBuildCmd(), newImageEditCmd(), newImageRmCmd())
	return cmd
}

// backendRemoveImage is the image-removal seam, overridable in tests so
// `image rm` can be exercised without a real engine.
var backendRemoveImage = backend.RemoveImage

// newImageEditCmd opens .sandboxer/image.nix in $EDITOR, scaffolding the
// annotated starter hook first when the file does not exist yet — so a user can
// go straight from "I want a custom image" to editing without hunting for the
// template.
func newImageEditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "edit",
		Short: "Edit " + imageNixPath() + " in $EDITOR (scaffolds it if missing)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := imageNixPath()
			if !fileExists(path) {
				if err := os.MkdirAll(config.StateDirName, 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(path, []byte(starterImageNix), 0o644); err != nil {
					return err
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "sandboxer: scaffolded %s\n", path)
			}
			return openInEditor(cmd, path)
		},
	}
	return cmd
}

// newImageRmCmd removes a built toolbox image: the stock default image, or —
// with a profile — that profile's content-addressed variant tag. Idempotent:
// an already-absent image is success.
func newImageRmCmd() *cobra.Command {
	var engineFlag, configPath string
	cmd := &cobra.Command{
		Use:   "rm [profile]",
		Short: "Remove a built toolbox image (stock, or a profile's variant)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := config.LoadDefaults()
			engine, err := backend.ResolveEngine(engineFlag, d)
			if err != nil {
				return err
			}
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
			if err := backendRemoveImage(engine, image); err != nil {
				return fmt.Errorf("remove image %s: %w", image, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed image: %s\n", image)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&engineFlag, "engine", "", "container engine: docker | podman (default: auto-detect)")
	fl.StringVarP(&configPath, "config", "f", "", "profile whose image variant to remove")
	return cmd
}

// openInEditor launches $VISUAL/$EDITOR on path (falling back to vi), wiring the
// child process to the command's stdio so an interactive editor works. Shared
// by `image edit` and `profile edit`.
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
