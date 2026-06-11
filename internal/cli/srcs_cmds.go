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
		Example: `  # pull deps in; idempotent — existing files are kept
  sandboxer pull feat

  # re-pull, overwriting local edits in the sandbox
  sandboxer pull feat --force`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if inContainer() {
				dir := os.Getenv("SANDBOXER_SANDBOX_DIR")
				if dir == "" {
					dir = getwd()
				}
				return srcs.CopyIn(out, "/run/sandboxer/profile.json", dir, "/run/sandboxer/manifest.json", f.force, true)
			}
			t, err := resolveTarget(f, posArg(args))
			if err != nil {
				return err
			}
			// Refresh the stored snapshot from the live profile first, so a pull
			// after editing .sandboxer/config.yaml sees the new roots/deps.
			if _, err := t.syncSnapshot(); err != nil {
				return err
			}
			pj := t.base.ProfileJSONPath(t.slug)
			if !fileExists(pj) {
				return fmt.Errorf("sandbox %q has no profile (nothing to pull)", t.slug)
			}
			return srcs.CopyIn(out, pj, t.base.SandboxDir(t.slug), t.base.ManifestPath(t.slug), f.force, false)
		},
	}
	bindExisting(cmd, &f)
	cmd.Flags().BoolVar(&f.force, "force", false, "overwrite locally modified targets")
	return cmd
}

func newPushCmd() *cobra.Command {
	var f commonFlags
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "push [slug]",
		Short: "Copy rw dependencies from the sandbox back to their origins",
		Example: `  # preview what would be overwritten (no files touched)
  sandboxer push feat --dry-run

  # return rw deps to their origins — OVERWRITES each origin wholesale
  sandboxer push feat`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if inContainer() {
				return srcs.CopyOut(out, "/run/sandboxer/manifest.json", dryRun)
			}
			t, err := resolveTarget(f, posArg(args))
			if err != nil {
				return err
			}
			mf := t.base.ManifestPath(t.slug)
			if !fileExists(mf) {
				return fmt.Errorf("sandbox %q has no manifest (nothing to return)", t.slug)
			}
			return srcs.CopyOut(out, mf, dryRun)
		},
	}
	bindExisting(cmd, &f)
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be pushed without changing files")
	return cmd
}
