package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/backend"
	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/registry"
)

func init() { register(newDoctorCmd) }

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

			// Config file in cwd.
			if fileExists(config.ConfigFileName) {
				if _, err := config.LoadDocument(config.ConfigFileName); err == nil {
					fmt.Fprintf(tw, "%s in cwd\t✓\tparses ok\n", config.ConfigFileName)
					ok++
				} else {
					fmt.Fprintf(tw, "%s in cwd\t⚠\t%v\n", config.ConfigFileName, err)
					warn++
				}
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

func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if len(p) > 1 && p[0] == '~' && p[1] == '/' {
		return filepath.Join(home, p[2:])
	}
	return p
}
