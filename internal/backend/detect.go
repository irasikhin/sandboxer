// Package backend runs an agent inside a podman/docker container built from the
// toolbox image, with a per-sandbox isolated home, dependency origin mounts and
// the egress allowlist.
package backend

import (
	"errors"
	"os"
	"os/exec"
	"strings"

	"github.com/irasikhin/sandboxer/internal/config"
)

// ResolveEngine picks the container engine to invoke. Precedence:
//
//   - SANDBOXER_ENGINE (d.Engine) wins outright;
//   - an explicitly requested engine (be == "podman"|"docker") is honored when
//     that binary is installed — so --backend podman actually runs podman even
//     when docker is also present;
//   - otherwise auto-detect, preferring docker then podman.
//
// A requested-but-missing engine is not an error: we fall back to whatever is
// installed so the default backend ("docker") still works on a podman-only host.
// Callers display the engine we return (see EngineLabel), so the banner never
// disagrees with what actually runs.
func ResolveEngine(be string, d config.Defaults) (string, error) {
	// The microVM backend resolves to smolvm, and does so BEFORE the
	// SANDBOXER_ENGINE override — that override names a container engine and must
	// never silently redirect a microVM sandbox onto docker, which would drop the
	// hardware isolation the backend was chosen for.
	if be == "microvm" {
		return resolveSmolvm()
	}
	if d.Engine != "" {
		return d.Engine, nil
	}
	if (be == "podman" || be == "docker") && hasExec(be) {
		return be, nil
	}
	if hasExec("docker") {
		return "docker", nil
	}
	if hasExec("podman") {
		return "podman", nil
	}
	return "", errors.New("need docker or podman " +
		"(NixOS: virtualisation.docker.enable or virtualisation.podman.enable)")
}

// DetectEngine resolves the engine with no explicit backend preference (the
// plain docker→podman auto-detect).
func DetectEngine(d config.Defaults) (string, error) { return ResolveEngine("", d) }

// InstalledEngines returns every engine a sandboxer-managed container could
// live on: the SANDBOXER_ENGINE override alone when set, otherwise each of
// podman and docker that is actually installed (possibly none). Sweeps and
// reports (rm-all, list, doctor) iterate over all of them — a profile's
// `backend:` may have created sessions on either engine, so probing only the
// auto-detected one would strand the other's containers.
func InstalledEngines(d config.Defaults) []string {
	if d.Engine != "" {
		return []string{d.Engine}
	}
	var engines []string
	for _, e := range []string{"podman", "docker"} {
		if hasExec(e) {
			engines = append(engines, e)
		}
	}
	return engines
}

// EngineLabel reports, best-effort, the engine ResolveEngine would pick, for the
// summary banner so it matches what runs. It never errors: with no engine
// installed it returns be unchanged so the banner still names the configured
// backend.
func EngineLabel(be string, d config.Defaults) string {
	if e, err := ResolveEngine(be, d); err == nil {
		return e
	}
	return be
}

// resolveSmolvm confirms the microVM backend can run and returns its ENGINE
// IDENTITY, the constant "smolvm" — never the override path. That identity is
// what flows through RunOpts.Engine and every (engine, name) call, so the
// backend dispatch (engine == smolvmEngine) stays robust even when
// SANDBOXER_SMOLVM points the actual binary somewhere off PATH; the real binary
// is resolved separately at exec time (smolvmBin). Like docker/podman smolvm is
// a preinstalled host requirement — an absent one is a clear error with an
// install hint, never an autodownload, and never a silent fallback to a
// container engine, which would quietly weaken isolation.
func resolveSmolvm() (string, error) {
	if hasExec(smolvmBin()) {
		return smolvmEngine, nil
	}
	return "", errors.New("the microvm backend needs smolvm on PATH " +
		"(install: https://smolmachines.com — SANDBOXER_SMOLVM overrides the path; " +
		"on NixOS the release binary is dynamically linked, run it via nix-ld or patchelf)")
}

// smolvmBin is the actual binary the microVM backend executes: the
// SANDBOXER_SMOLVM override (a name or an absolute path), else "smolvm" resolved
// on PATH. Distinct from the engine identity smolvmEngine, which is only ever
// the constant "smolvm" (see resolveSmolvm).
func smolvmBin() string {
	if b := os.Getenv("SANDBOXER_SMOLVM"); b != "" {
		return b
	}
	return smolvmEngine
}

// SmolvmStatus reports the microVM backend's host readiness for `doctor`:
// present is whether the smolvm binary is found (via smolvmBin), version is its
// `--version` line when it runs, and kvmOK is whether /dev/kvm exists on Linux
// (always true off Linux, where the platform hypervisor is used instead).
func SmolvmStatus() (present bool, version string, kvmOK bool) {
	kvmOK = devKVMPresent()
	if !hasExec(smolvmBin()) {
		return false, "", kvmOK
	}
	present = true
	if out, err := exec.Command(smolvmBin(), "--version").Output(); err == nil {
		version = strings.TrimSpace(string(out))
	}
	return present, version, kvmOK
}

func hasExec(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
