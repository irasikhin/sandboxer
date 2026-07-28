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
// hides sandboxer.nix. The config is meant to be committed (runtime state
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

			// Host nix — required to evaluate sandboxer.nix (eval only; images
			// still build inside a container).
			if _, err := exec.LookPath("nix-instantiate"); err != nil {
				fmt.Fprintf(tw, "nix\t⚠\tnot found — required to evaluate %s (install: https://nixos.org/download)\n",
					config.ConfigFileName)
				warn++
			} else {
				fmt.Fprintf(tw, "nix\t✓\tnix-instantiate available\n")
				ok++
			}

			// Container engine.
			engine, engineErr := backend.DetectEngine(d)
			if engineErr != nil {
				fmt.Fprintf(tw, "container engine\t⚠\t%s\n", engineErr)
				warn++
			} else {
				fmt.Fprintf(tw, "container engine\t✓\t%s available\n", engine)
				ok++

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

			// microVM backends (smolvm, microsandbox). Reported alongside the
			// container engine so a host set up for one of them gets the same
			// at-a-glance check.
			reportMicrovm(tw, &ok, &warn)
			reportMicrosandbox(tw, &ok, &warn)

			// Persistent sessions: this project's running/stopped tally, plus
			// orphans whose project dir is gone (their containers survive a
			// bare `rm -rf` of the project). Every installed engine is probed —
			// per-profile backends may have put sessions on podman AND docker.
			for _, e := range backendSweepEngines(d) {
				reportSessions(tw, e, &ok, &warn)
			}

			// Agents baked into the toolbox image. Credentials are NEVER passed
			// through from the host: log in or export API keys INSIDE the
			// sandbox (its private $HOME persists).
			for _, name := range registry.Names() {
				a, _ := registry.Get(name)
				fmt.Fprintf(tw, "agent %s\t-\tbin=%s image=%v (auth inside the sandbox)\n",
					name, a.Bin, a.Image == nil || *a.Image)
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
			case fileExists(config.LegacyYAMLConfigFileName):
				// An upgrading user with the YAML-era config: translate by hand.
				fmt.Fprintf(tw, "./%s\t⚠\tlegacy YAML config — translate it to %s (same camelCase keys; no longer read)\n",
					config.LegacyYAMLConfigFileName, cfgPath)
				warn++
			case fileExists(config.LegacyConfigDirPath()):
				// An upgrading user with the pre-relocation .sandboxer/config.yaml.
				fmt.Fprintf(tw, "%s\t⚠\tlegacy location — translate it to %s (no longer read)\n",
					config.LegacyConfigDirPath(), cfgPath)
				warn++
			case fileExists(config.LegacyConfigFileName):
				// An upgrading user with the ancient root-level profile: flag the move.
				fmt.Fprintf(tw, "./%s\t⚠\tlegacy location — translate it to %s (no longer read)\n",
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

// reportMicrovm adds doctor's microVM-backend row: whether smolvm is installed
// (with its version), and on Linux whether /dev/kvm is present. A missing
// smolvm is a warning, not an error — a host using only the container backend
// does not need it — with an install hint; a present smolvm without KVM is
// flagged because the backend cannot boot a machine there.
func reportMicrovm(tw io.Writer, ok, warn *int) {
	present, version, kvmOK := backend.SmolvmStatus()
	switch {
	case !present:
		fmt.Fprintf(tw, "microvm (smolvm)\t⚠\tnot found — needed only for backend: microvm (install: https://smolmachines.com; SANDBOXER_SMOLVM overrides the path)\n")
		*warn++
	case !kvmOK:
		msg := "the microvm backend needs KVM on Linux"
		if underWSL() {
			msg = "under WSL2 — enable nested KVM: set nestedVirtualization=true in %UserProfile%\\.wslconfig, then `wsl --shutdown` (see docs/windows.md)"
		}
		fmt.Fprintf(tw, "microvm (smolvm)\t⚠\t%s present, but /dev/kvm is missing — %s\n", version, msg)
		*warn++
	default:
		fmt.Fprintf(tw, "microvm (smolvm)\t✓\t%s available\n", version)
		*ok++
	}
}

// reportMicrosandbox adds doctor's row for the second microVM runner. Same
// shape as reportMicrovm — a missing msb is only a warning — plus the one
// prerequisite that is otherwise invisible until the first `create` fails: every
// sandbox's agent-relay UNIX socket path derives from MSB_HOME, and a deep home
// pushes it past the kernel's 108-byte limit.
func reportMicrosandbox(tw io.Writer, ok, warn *int) {
	present, version, kvmOK, homeOK := backend.MsbStatus()
	switch {
	case !present:
		fmt.Fprintf(tw, "microsandbox (msb)\t⚠\tnot found — needed only for backend: microsandbox (install: https://microsandbox.dev; SANDBOXER_MSB overrides the path)\n")
		*warn++
	case !kvmOK:
		msg := "the microsandbox backend needs KVM on Linux"
		if underWSL() {
			msg = "under WSL2 — enable nested KVM: set nestedVirtualization=true in %UserProfile%\\.wslconfig, then `wsl --shutdown` (see docs/windows.md)"
		}
		fmt.Fprintf(tw, "microsandbox (msb)\t⚠\t%s present, but /dev/kvm is missing — %s\n", version, msg)
		*warn++
	case !homeOK:
		fmt.Fprintf(tw, "microsandbox (msb)\t⚠\t%s present, but MSB_HOME is too deep (%s) — every sandbox's agent socket derives from it and must stay under 108 bytes; point MSB_HOME at a short path\n",
			version, backend.MsbHome())
		*warn++
	default:
		fmt.Fprintf(tw, "microsandbox (msb)\t✓\t%s available\n", version)
		*ok++
	}
}

// underWSL reports whether this process runs inside a WSL distro, so doctor can
// point a missing-/dev/kvm microVM user at the nested-virtualization setting
// rather than at a plain "install KVM". Best-effort: WSL marks itself in
// /proc/version ("microsoft"/"WSL").
func underWSL() bool {
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	v := strings.ToLower(string(data))
	return strings.Contains(v, "microsoft") || strings.Contains(v, "wsl")
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
