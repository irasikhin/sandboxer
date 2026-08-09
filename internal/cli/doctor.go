package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/backend"
	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/registry"
	"github.com/irasikhin/sandboxer/internal/sandbox"
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

// doctorCheck is one doctor finding, shared by the table and --json
// renderings. Status is "ok", "warn" or "info".
type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

func okCheck(name, detail string) doctorCheck   { return doctorCheck{name, "ok", detail} }
func warnCheck(name, detail string) doctorCheck { return doctorCheck{name, "warn", detail} }

func newDoctorCmd() *cobra.Command {
	var strict, asJSON bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check the environment and report any issues",
		Long: `Check that the sandboxer environment is correctly set up: container engine,
toolbox image, agent credentials, and common configuration issues.

Run this after a fresh install or when something isn't working. With
--strict, any warning makes doctor exit non-zero, so it can gate a CI
or provisioning pipeline; --json emits the checks machine-readably.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			checks := doctorChecks()
			ok, warn := 0, 0
			for _, c := range checks {
				switch c.Status {
				case "ok":
					ok++
				case "warn":
					warn++
				}
			}
			out := cmd.OutOrStdout()
			if asJSON {
				doc := struct {
					Checks   []doctorCheck `json:"checks"`
					OK       int           `json:"ok"`
					Warnings int           `json:"warnings"`
				}{checks, ok, warn}
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				if err := enc.Encode(doc); err != nil {
					return err
				}
			} else {
				tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
				symbols := map[string]string{"ok": "✓", "warn": "⚠", "info": "-"}
				for _, c := range checks {
					fmt.Fprintf(tw, "%s\t%s\t%s\n", c.Name, symbols[c.Status], c.Detail)
				}
				if err := tw.Flush(); err != nil {
					return err
				}
				fmt.Fprintf(out, "\n%d ok, %d warning(s)\n", ok, warn)
			}
			if strict && warn > 0 {
				// The tally above is the diagnostic; silentErr keeps Run from
				// narrating it a second time.
				return silentErr{fmt.Errorf("%d warning(s)", warn)}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&strict, "strict", false, "exit non-zero when any warning is found (for CI preflight)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit machine-readable JSON instead of the table")
	return cmd
}

// doctorChecks runs every check and returns the findings in display order.
func doctorChecks() []doctorCheck {
	var checks []doctorCheck
	d := config.LoadDefaults()

	// Host nix — required to evaluate sandboxer.nix (eval only; images
	// still build inside a container).
	if _, err := exec.LookPath("nix-instantiate"); err != nil {
		checks = append(checks, warnCheck("nix",
			fmt.Sprintf("not found — required to evaluate %s (install: https://nixos.org/download)", config.ConfigFileName)))
	} else {
		checks = append(checks, okCheck("nix", "nix-instantiate available"))
	}

	// Host git — every source is checked out as a git worktree, and without
	// git the srcs errors are misleading ("not a git repository with a
	// commit" also covers a missing git binary).
	if _, err := exec.LookPath("git"); err != nil {
		checks = append(checks, warnCheck("git", "not found — every source is checked out as a git worktree; install git"))
	} else {
		checks = append(checks, okCheck("git", "available"))
	}

	// Container engine. Missing docker/podman is only a problem if a profile
	// still resolves to a container backend — the microVM backends never touch
	// one, so a docker/podman-less host running every profile on a microVM is
	// healthy (that is the migration `doctor` is here to shepherd). The
	// per-profile backend check below is what flags the profiles that would
	// still fail.
	engine, engineErr := backend.DetectEngine(d)
	if engineErr != nil {
		detail := engineErr.Error()
		status := "warn"
		if vmRunnerInstalled(d) {
			detail = "no docker/podman — fine if every profile selects a microVM backend " +
				"(microvm/microsandbox); the default backend is still docker, so a profile " +
				"without `backend:` needs one"
			status = "info"
		}
		checks = append(checks, doctorCheck{"container engine", status, detail})

		// On a VM-only host the engine-present branch below never runs, so the
		// toolbox-image check would be silently missing. Report it from the
		// microVM store (the shared tar both runners boot from) instead.
		if engine := firstVMEngine(d); engine != "" {
			image := d.Image
			if backend.ImageExists(engine, image) {
				checks = append(checks, okCheck("toolbox image "+image, "present in the microVM store"))
			} else {
				checks = append(checks, warnCheck("toolbox image "+image,
					"not found — build with: sandboxer image build --backend "+engine))
			}
		}
	} else {
		checks = append(checks, okCheck("container engine", engine+" available"))

		// Toolbox image.
		image := d.Image
		if backend.ImageExists(engine, image) {
			checks = append(checks, okCheck("toolbox image "+image, "present"))
		} else {
			checks = append(checks, warnCheck("toolbox image "+image, "not found — build with: sandboxer image build"))
		}
	}

	// microVM backends (smolvm, microsandbox). Reported alongside the
	// container engine so a host set up for one of them gets the same
	// at-a-glance check.
	checks = append(checks, reportMicrovm(), reportMicrosandbox())

	// Persistent sessions: this project's running/stopped tally, plus
	// orphans whose project dir is gone (their containers survive a
	// bare `rm -rf` of the project). Every installed engine is probed —
	// per-profile backends may have put sessions on podman AND docker.
	for _, e := range backendSweepEngines(d) {
		checks = append(checks, reportSessions(e)...)
	}

	// Agents baked into the toolbox image. Credentials are NEVER passed
	// through from the host: log in or export API keys INSIDE the
	// sandbox (its private $HOME persists).
	for _, name := range registry.Names() {
		a, _ := registry.Get(name)
		checks = append(checks, doctorCheck{"agent " + name, "info",
			fmt.Sprintf("bin=%s image=%v (auth inside the sandbox)", a.Bin, a.Image == nil || *a.Image)})
	}

	// Project config at the repo root.
	cfgPath := config.ConfigPath()
	switch {
	case fileExists(cfgPath):
		if doc, err := config.LoadDocument(cfgPath); err == nil {
			checks = append(checks, okCheck(cfgPath, "parses ok"))
			checks = append(checks, nestedChecks(doc, d)...)
			checks = append(checks, profileBackendChecks(doc, d)...)
		} else {
			checks = append(checks, warnCheck(cfgPath, err.Error()))
		}
		// The config is meant to be committed; an ignore rule covering it
		// would silently keep it out of git.
		if gitCheckIgnore(getwd(), cfgPath) {
			checks = append(checks, warnCheck(cfgPath, "ignored by the repo's gitignore — drop that rule so the config can be committed"))
		}
	case fileExists(config.LegacyYAMLConfigFileName):
		// An upgrading user with the YAML-era config: translate by hand.
		checks = append(checks, warnCheck("./"+config.LegacyYAMLConfigFileName,
			fmt.Sprintf("legacy YAML config — translate it to %s (same camelCase keys; no longer read)", cfgPath)))
	case fileExists(config.LegacyConfigDirPath()):
		// An upgrading user with the pre-relocation .sandboxer/config.yaml.
		checks = append(checks, warnCheck(config.LegacyConfigDirPath(),
			fmt.Sprintf("legacy location — translate it to %s (no longer read)", cfgPath)))
	case fileExists(config.LegacyConfigFileName):
		// An upgrading user with the ancient root-level profile: flag the move.
		checks = append(checks, warnCheck("./"+config.LegacyConfigFileName,
			fmt.Sprintf("legacy location — translate it to %s (no longer read)", cfgPath)))
	}

	// Pre-split leftovers: runtime state used to live under .sandboxer/;
	// it now lives outside the repo (config.StateDir). Flag the old dirs
	// so an upgrading user can delete them.
	if legacyStateLeftover(getwd()) {
		checks = append(checks, warnCheck(config.LegacyStateDirName+"/_meta",
			fmt.Sprintf("pre-split runtime state — data now lives in %s; safe to delete the old _meta/_home/_logs/<slug> dirs", config.StateDir(getwd()))))
	}
	return checks
}

// hostSubIDCounts is the subordinate-range lookup seam — the real one reads
// the HOST's /etc/subuid, whose content a test cannot control.
var hostSubIDCounts = sandbox.HostSubIDCounts

// nestedChecks reports, per profile that opted into nestedContainers, whether
// the host can actually give it a MULTI-uid nested podman — the difference
// between "podman run postgres" working and dying with EINVAL. Silent for
// profiles that did not opt in, and for microVM backends (where the knob is
// warn-and-ignored: a VM runs container engines natively).
func nestedChecks(doc *config.Document, d config.Defaults) []doctorCheck {
	var checks []doctorCheck
	for _, name := range slices.Sorted(maps.Keys(doc.Profiles)) {
		p := doc.Profiles[name]
		if !p.NestedContainers {
			continue
		}
		row := "nestedContainers (" + name + ")"
		backendName := firstNonEmpty(p.Backend, d.Backend)
		if config.IsMicrovmBackend(backendName) {
			checks = append(checks, doctorCheck{row, "info",
				"ignored on " + backendName + " — a microVM runs container engines natively"})
			continue
		}
		engine, err := backend.ResolveEngine(backendName, d)
		if err != nil {
			checks = append(checks, warnCheck(row, err.Error()))
			continue
		}
		if engine != "podman" {
			checks = append(checks, doctorCheck{row, "info",
				"nested podman is single-uid on a " + engine + " engine — images that switch user " +
					"(postgres) need backend = \"podman\""})
			continue
		}
		if uids, gids := hostSubIDCounts(); uids == 0 || gids == 0 {
			checks = append(checks, warnCheck(row,
				"no subordinate uid/gid ranges for this user (/etc/subuid, /etc/subgid) — the nested "+
					"podman stays single-uid, so images that switch user won't run; grant a range "+
					"(usermod --add-subuids/--add-subgids)"))
			continue
		}
		checks = append(checks, okCheck(row, "podman engine + host subordinate ranges — multi-uid nested containers work"))
	}
	return checks
}

// vmRunnerInstalled reports whether any microVM runner is actually installed on
// this host, so doctor can tell a healthy docker/podman-less VM-only host from a
// host with no isolation engine at all.
func vmRunnerInstalled(d config.Defaults) bool {
	for _, e := range backend.SweepEngines(d) {
		if backend.IsVMEngine(e) {
			return true
		}
	}
	return false
}

// firstVMEngine returns the engine identity of the first installed microVM
// runner, or "" when none is installed. It backs the VM-only toolbox-image row
// (the tar store check is runner-independent, so the first installed runner is
// as good as any).
func firstVMEngine(d config.Defaults) string {
	for _, e := range backend.SweepEngines(d) {
		if backend.IsVMEngine(e) {
			return e
		}
	}
	return ""
}

// profileBackendChecks reports, per profile, whether its effective backend can
// actually run on this host — the migration signal `doctor` exists to surface.
// A profile whose `backend:` (or the default) still names an engine that is not
// installed will fail at enter/exec, and the error would otherwise come late
// and out of context. Silent for profiles whose backend resolves.
func profileBackendChecks(doc *config.Document, d config.Defaults) []doctorCheck {
	var checks []doctorCheck
	for _, name := range slices.Sorted(maps.Keys(doc.Profiles)) {
		p := doc.Profiles[name]
		backendName := firstNonEmpty(p.Backend, d.Backend)
		if _, err := backend.ResolveEngine(backendName, d); err != nil {
			checks = append(checks, warnCheck("profile "+name,
				fmt.Sprintf("backend %q cannot run here: %v", backendName, err)))
		}
	}
	return checks
}

// reportMicrovm builds doctor's microVM-backend row: whether smolvm is
// installed (with its version), and on Linux whether /dev/kvm is present. A
// missing smolvm is a warning, not an error — a host using only the container
// backend does not need it — with an install hint; a present smolvm without
// KVM is flagged because the backend cannot boot a machine there.
func reportMicrovm() doctorCheck {
	const name = "microvm (smolvm)"
	present, version, kvmOK := backend.SmolvmStatus()
	switch {
	case !present:
		return warnCheck(name, "not found — needed only for backend: microvm (install: https://smolmachines.com; SANDBOXER_SMOLVM overrides the path)")
	case !kvmOK:
		msg := "the microvm backend needs KVM on Linux"
		if underWSL() {
			msg = "under WSL2 — enable nested KVM: set nestedVirtualization=true in %UserProfile%\\.wslconfig, then `wsl --shutdown` (see docs/windows.md)"
		}
		return warnCheck(name, fmt.Sprintf("%s present, but /dev/kvm is missing — %s", version, msg))
	default:
		return okCheck(name, version+" available")
	}
}

// reportMicrosandbox builds doctor's row for the second microVM runner. Same
// shape as reportMicrovm — a missing msb is only a warning — plus the one
// prerequisite that is otherwise invisible until the first `create` fails: every
// sandbox's agent-relay UNIX socket path derives from MSB_HOME, and a deep home
// pushes it past the kernel's 108-byte limit.
func reportMicrosandbox() doctorCheck {
	const name = "microsandbox (msb)"
	present, version, kvmOK, homeOK := backend.MsbStatus()
	switch {
	case !present:
		return warnCheck(name, "not found — needed only for backend: microsandbox (install: https://microsandbox.dev; SANDBOXER_MSB overrides the path)")
	case !kvmOK:
		msg := "the microsandbox backend needs KVM on Linux"
		if underWSL() {
			msg = "under WSL2 — enable nested KVM: set nestedVirtualization=true in %UserProfile%\\.wslconfig, then `wsl --shutdown` (see docs/windows.md)"
		}
		return warnCheck(name, fmt.Sprintf("%s present, but /dev/kvm is missing — %s", version, msg))
	case !homeOK:
		return warnCheck(name, fmt.Sprintf("%s present, but MSB_HOME is too deep (%s) — every sandbox's agent socket derives from it and must stay under 108 bytes; point MSB_HOME at a short path",
			version, backend.MsbHome()))
	default:
		return okCheck(name, version+" available")
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

// reportSessions builds doctor's persistent-session rows for one engine: a
// running/stopped tally for the current project, and a warning listing
// orphaned session containers — sandboxer-managed but with their
// sandboxer.base directory gone — together with a removal hint. The orphan
// probe is purely advisory, so its failure is not a finding.
func reportSessions(engine string) []doctorCheck {
	baseDir := config.StateDir(getwd())
	states, err := sessionStates(engine, baseDir)
	if err != nil {
		return []doctorCheck{warnCheck("sessions ("+engine+")", err.Error())}
	}
	running := 0
	for _, st := range states {
		if st == "running" {
			running++
		}
	}
	checks := []doctorCheck{okCheck("sessions ("+engine+")",
		fmt.Sprintf("%d running / %d stopped for this project", running, len(states)-running))}
	orphans, err := sessionOrphans(engine)
	if err != nil || len(orphans) == 0 {
		return checks
	}
	return append(checks, warnCheck("orphan sessions ("+engine+")",
		fmt.Sprintf("%s — their projects are gone; remove: %s",
			strings.Join(orphans, " "), backend.RemoveCommand(engine, orphans))))
}

// legacyStateLeftover reports whether the pre-split runtime state directory
// (<root>/.sandboxer/_meta) is still present — runtime state has moved to
// config.StateDir, so its lingering presence is worth a one-line cleanup hint.
func legacyStateLeftover(root string) bool {
	return fileExists(filepath.Join(root, config.LegacyStateDirName, "_meta"))
}
