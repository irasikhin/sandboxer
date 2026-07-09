package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/srcs"
	"github.com/irasikhin/sandboxer/internal/worktree"
)

func init() {
	register(newPullCmd)
	register(newPushCmd)
}

// projectRootFromSandbox derives the host project root from the sandbox dir
// (<root>/.sandboxer/<slug>) — used in-container, where there is no host cwd to
// resolve against. It returns "" when the layout doesn't match; context entries
// then simply resolve to nothing.
func projectRootFromSandbox(dir string) string {
	parent := filepath.Dir(dir)
	if filepath.Base(parent) != config.StateDirName {
		return ""
	}
	return filepath.Dir(parent)
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
				return srcs.CopyIn(out, srcs.PullOpts{
					ProfileFile:  "/run/sandboxer/profile.json",
					SandboxDir:   dir,
					ManifestFile: "/run/sandboxer/manifest.json",
					ProjectRoot:  projectRootFromSandbox(dir),
					Force:        f.force,
					InContainer:  true,
				})
			}
			t, err := resolveTarget(f, posArg(args))
			if err != nil {
				return err
			}
			if t.base.RepoRoot != "" {
				return fmt.Errorf("%q is a git worktree — pull applies to copy-mode sandboxes only; edit deps then 'sandboxer recreate'", t.slug)
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
			return srcs.CopyIn(out, srcs.PullOpts{
				ProfileFile:  pj,
				SandboxDir:   t.base.SandboxDir(t.slug),
				ManifestFile: t.base.ManifestPath(t.slug),
				ProjectRoot:  t.base.Src,
				Force:        f.force,
			})
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
		Short: "Copy rw dependencies from the sandbox back to their origins (skip host-modified; --force overwrites)",
		Example: `  # preview what would be overwritten (no files touched)
  sandboxer push feat --dry-run

  # return rw deps to their origins; an origin edited on the host since the
  # pull is skipped with a warning
  sandboxer push feat

  # overwrite each origin wholesale, host edits included
  sandboxer push feat --force`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if inContainer() {
				return srcs.CopyOut(out, "/run/sandboxer/manifest.json", srcs.PushOpts{DryRun: dryRun, Force: f.force})
			}
			t, err := resolveTarget(f, posArg(args))
			if err != nil {
				return err
			}
			if t.base.RepoRoot != "" {
				return fmt.Errorf("%q is a git worktree — push applies to copy-mode sandboxes only; the agent's work is already on branch %s", t.slug, worktree.Branch(t.slug))
			}
			mf := t.base.ManifestPath(t.slug)
			if !fileExists(mf) {
				return fmt.Errorf("sandbox %q has no manifest (nothing to return)", t.slug)
			}
			return srcs.CopyOut(out, mf, srcs.PushOpts{DryRun: dryRun, Force: f.force})
		},
	}
	bindExisting(cmd, &f)
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be pushed without changing files")
	cmd.Flags().BoolVar(&f.force, "force", false, "overwrite origins that changed on the host since pull")
	return cmd
}
