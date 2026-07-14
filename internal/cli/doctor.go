package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/backend"
	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/registry"
)

func init() { register(newDoctorCmd) }

// sessionOrphans is the orphan-enumeration seam, overridable in tests so
// doctor's session rows render without a real engine.
var sessionOrphans = backend.OrphanSessions

// gitCheckIgnore reports whether rel is ignored by the repo's gitignore rules
// at root (exit 0 = ignored; 1 = not; no git / not a repo = not ignored — the
// check is purely advisory). Overridable in tests.
var gitCheckIgnore = func(root, rel string) bool {
	return exec.Command("git", "-C", root, "check-ignore", "-q", "--", rel).Run() == nil
}

// warnIgnoredConfig prints a one-line advisory when the user's repo gitignore
// hides sandboxer.yaml. The config is meant to be committed (runtime state
// lives outside the repo), so an ignore rule covering it would silently keep
// it out of git.
func warnIgnoredConfig(w io.Writer, root string) {
	rel := config.ConfigPath()
	if !fileExists(filepath.Join(root, rel)) || !gitCheckIgnore(root, rel) {
		return
	}
	fmt.Fprintf(w, "sandboxer: %s is ignored by your repo's gitignore — drop that rule so the config can be committed (runtime state lives outside the repo)\n",
		rel)
}

func newDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check the environment and report any issues",
		Long: `Check that the sandboxer environment is correctly set up: container engine,
toolbox image, agent credentials, and common configuration issues.

Run this after a fresh install or when something isn't working.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			ok, warn := 0, 0

			d := config.LoadDefaults()

			// Container engine.
			engine, engineErr := backend.DetectEngine(d)
			if engineErr != nil {
				fmt.Fprintf(tw, "container engine\t⚠\t%s\n", engineErr)
				warn++
			} else {
				fmt.Fprintf(tw, "container engine\t✓\t%s available\n", engine)

				// Toolbox image.
				image := d.Image
				if backend.ImageExists(engine, image) {
					fmt.Fprintf(tw, "toolbox image %s\t✓\tpresent\n", image)
					ok++
				} else {
					fmt.Fprintf(tw, "toolbox image %s\t⚠\tnot found — build with: sandboxer image build\n", image)
					warn++
				}
			}

			// Persistent sessions: this project's running/stopped tally, plus
			// orphans whose project dir is gone (their containers survive a
			// bare `rm -rf` of the project). Every installed engine is probed —
			// per-profile backends may have put sessions on podman AND docker.
			for _, e := range backendInstalledEngines(d) {
				reportSessions(tw, e, &ok, &warn)
			}

			// Agent credentials — check ONLY the auth env vars each agent reads.
			// Host credential DIRECTORIES are deliberately never mounted (every
			// sandbox has an isolated $HOME), so an absent env var is not a
			// problem: the recommended path is to log in INSIDE the sandbox. We
			// report presence, never a scary "no creds" for that normal flow.
			for _, name := range registry.Names() {
				a, _ := registry.Get(name)
				found := false
				for _, e := range a.AuthEnv {
					if os.Getenv(e) != "" {
						found = true
						break
					}
				}
				status := "✓ auth env set"
				if !found {
					status = "- no auth env (or log in inside the sandbox)"
				}
				fmt.Fprintf(tw, "agent %s\t%s\tbin=%s image=%v\n",
					name, status, a.Bin, a.Image == nil || *a.Image)
			}

			// Profile store.
			pd := config.ProfilesDir()
			if pd != "" {
				refs := config.ListProfilesIn(pd)
				if len(refs) > 0 {
					fmt.Fprintf(tw, "profile store %s\t✓\t%d profile(s)\n", pd, len(refs))
					ok++
				} else {
					fmt.Fprintf(tw, "profile store %s\t-\tempty (drop .yaml files here for named profiles)\n", pd)
					ok++
				}
			}

			// Project config at the repo root.
			cfgPath := config.ConfigPath()
			switch {
			case fileExists(cfgPath):
				if _, err := config.LoadDocument(cfgPath); err == nil {
					fmt.Fprintf(tw, "%s\t✓\tparses ok\n", cfgPath)
					ok++
				} else {
					fmt.Fprintf(tw, "%s\t⚠\t%v\n", cfgPath, err)
					warn++
				}
				// The config is meant to be committed; an ignore rule covering it
				// would silently keep it out of git.
				if gitCheckIgnore(getwd(), cfgPath) {
					fmt.Fprintf(tw, "%s\t⚠\tignored by the repo's gitignore — drop that rule so the config can be committed\n",
						cfgPath)
					warn++
				}
			case fileExists(config.LegacyConfigDirPath()):
				// An upgrading user with the pre-relocation .sandboxer/config.yaml.
				fmt.Fprintf(tw, "%s\t⚠\tlegacy location — git mv it to %s (no longer read)\n",
					config.LegacyConfigDirPath(), cfgPath)
				warn++
			case fileExists(config.LegacyConfigFileName):
				// An upgrading user with the ancient root-level profile: flag the move.
				fmt.Fprintf(tw, "./%s\t⚠\tlegacy location — move it to %s (no longer read)\n",
					config.LegacyConfigFileName, cfgPath)
				warn++
			}

			// Pre-split leftovers: runtime state used to live under .sandboxer/;
			// it now lives outside the repo (config.StateDir). Flag the old dirs
			// so an upgrading user can delete them.
			if legacyStateLeftover(getwd()) {
				fmt.Fprintf(tw, "%s/_meta\t⚠\tpre-split runtime state — data now lives in %s; safe to delete the old _meta/_home/_logs/<slug> dirs\n",
					config.LegacyStateDirName, config.StateDir(getwd()))
				warn++
			}

			if err := tw.Flush(); err != nil {
				return err
			}
			fmt.Fprintf(out, "\n%d ok, %d warning(s)\n", ok, warn)
			return nil
		},
	}
	return cmd
}

// reportSessions adds doctor's persistent-session rows for one engine: a
// running/stopped tally for the current project, and a warning listing
// orphaned session containers — sandboxer-managed but with their
// sandboxer.base directory gone — together with a removal hint. The orphan
// probe is purely advisory, so its failure is not a finding.
func reportSessions(tw io.Writer, engine string, ok, warn *int) {
	baseDir := config.StateDir(getwd())
	states, err := sessionStates(engine, baseDir)
	if err != nil {
		fmt.Fprintf(tw, "sessions (%s)\t⚠\t%v\n", engine, err)
		*warn++
		return
	}
	running := 0
	for _, st := range states {
		if st == "running" {
			running++
		}
	}
	fmt.Fprintf(tw, "sessions (%s)\t✓\t%d running / %d stopped for this project\n",
		engine, running, len(states)-running)
	*ok++
	orphans, err := sessionOrphans(engine)
	if err != nil || len(orphans) == 0 {
		return
	}
	fmt.Fprintf(tw, "orphan sessions (%s)\t⚠\t%s — their projects are gone; remove: %s rm -f %s\n",
		engine, strings.Join(orphans, " "), engine, strings.Join(orphans, " "))
	*warn++
}

// legacyStateLeftover reports whether the pre-split runtime state directory
// (<root>/.sandboxer/_meta) is still present — runtime state has moved to
// config.StateDir, so its lingering presence is worth a one-line cleanup hint.
func legacyStateLeftover(root string) bool {
	return fileExists(filepath.Join(root, config.LegacyStateDirName, "_meta"))
}
