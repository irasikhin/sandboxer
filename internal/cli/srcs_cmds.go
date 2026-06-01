package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/srcs"
)

func init() {
	register(newPullCmd)
	register(newPushCmd)
}

func newPullCmd() *cobra.Command {
	var f commonFlags
	cmd := &cobra.Command{
		Use:   "pull [slug]",
		Short: "Copy dependency origins into the sandbox (skip locally modified; --force overwrites)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if inContainer() {
				dir := os.Getenv("SANDBOXER_SANDBOX_DIR")
				if dir == "" {
					dir = getwd()
				}
				return srcs.CopyIn(out, "/run/sandboxer/profile.json", dir, "/run/sandboxer/manifest.json", f.force)
			}
			t, err := resolveTarget(f, posArg(args))
			if err != nil {
				return err
			}
			pj := t.base.ProfileJSONPath(t.slug)
			if !fileExists(pj) {
				return fmt.Errorf("sandbox %q has no profile (nothing to pull)", t.slug)
			}
			return srcs.CopyIn(out, pj, t.base.SandboxDir(t.slug), t.base.ManifestPath(t.slug), f.force)
		},
	}
	bindExisting(cmd, &f)
	cmd.Flags().BoolVar(&f.force, "force", false, "overwrite locally modified targets")
	return cmd
}

func newPushCmd() *cobra.Command {
	var f commonFlags
	cmd := &cobra.Command{
		Use:   "push [slug]",
		Short: "Copy rw dependencies from the sandbox back to their origins",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if inContainer() {
				return srcs.CopyOut(out, "/run/sandboxer/manifest.json")
			}
			t, err := resolveTarget(f, posArg(args))
			if err != nil {
				return err
			}
			mf := t.base.ManifestPath(t.slug)
			if !fileExists(mf) {
				return fmt.Errorf("sandbox %q has no manifest (nothing to return)", t.slug)
			}
			return srcs.CopyOut(out, mf)
		},
	}
	bindExisting(cmd, &f)
	return cmd
}
