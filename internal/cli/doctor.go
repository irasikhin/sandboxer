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
// hides .sandboxer/config.yaml: git cannot re-include a file under an ignored
// directory, so a root-level ".sandboxer/" rule defeats the generated
// .sandboxer/.gitignore allowlist and the profile/image hook would silently
// never be committed.
func warnIgnoredConfig(w io.Writer, root string) {
	rel := config.ConfigPath()
	if !fileExists(filepath.Join(root, rel)) || !gitCheckIgnore(root, rel) {
		return
	}
	fmt.Fprintf(w, "sandboxer: %s is ignored by your repo's gitignore (a %q rule?) — the %s/.gitignore allowlist can't override it; drop that rule so config.yaml/image.nix can be committed\n",
		rel, config.StateDirName+"/", config.StateDirName)
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
			home, _ := os.UserHomeDir()

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
					fmt.Fprintf(tw, "toolbox image %s\t⚠\tnot found — build with: sandboxer build-image\n", image)
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

			// Agent credentials — check config dirs and env vars for each agent.
			for _, name := range registry.Names() {
				a, _ := registry.Get(name)
				found := false
				for _, dir := range a.AuthConfigDirs {
					p := expandHome(dir.Path, home)
					if fileExists(p) {
						found = true
						break
					}
				}
				if !found {
					for _, e := range a.AuthEnv {
						if os.Getenv(e) != "" {
							found = true
							break
						}
					}
				}
				status := "✓"
				if !found {
					status = "⚠ (no creds found)"
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

			// Project config under .sandboxer/.
			cfgPath := config.ConfigPath()
			if fileExists(cfgPath) {
				if _, err := config.LoadDocument(cfgPath); err == nil {
					fmt.Fprintf(tw, "%s\t✓\tparses ok\n", cfgPath)
					ok++
				} else {
					fmt.Fprintf(tw, "%s\t⚠\t%v\n", cfgPath, err)
					warn++
				}
				// The allowlist .sandboxer/.gitignore commits config.yaml/image.nix,
				// but git can't re-include files under an ignored DIRECTORY — a
				// user-level ".sandboxer/" rule silently defeats it.
				if gitCheckIgnore(getwd(), cfgPath) {
					fmt.Fprintf(tw, "%s\t⚠\tignored by the repo's gitignore — a %q rule defeats the allowlist; drop it so config.yaml/image.nix can be committed\n",
						cfgPath, config.StateDirName+"/")
					warn++
				}
			} else if fileExists(config.LegacyConfigFileName) {
				// An upgrading user with the stale root-level profile: flag the move.
				fmt.Fprintf(tw, "./%s\t⚠\tlegacy location — move it to %s (no longer read)\n",
					config.LegacyConfigFileName, cfgPath)
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
	baseDir := filepath.Join(getwd(), config.StateDirName)
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

func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if len(p) > 1 && p[0] == '~' && p[1] == '/' {
		return filepath.Join(home, p[2:])
	}
	return p
}
