// Package backend runs an agent inside a microVM built from the toolbox
// image — a real machine per sandbox on libkrun (smolvm or microsandbox),
// with a per-sandbox isolated home, source shares and the egress allowlist.
package backend

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/irasikhin/sandboxer/internal/config"
)

// ResolveEngine picks the runner a backend name resolves to:
//
//   - "microvm" → smolvm;
//   - "microsandbox" → msb.
//
// Anything else is an error — the docker/podman container backend was removed,
// so a legacy backend name gets the migration hint instead of a silent
// fallback (config.ValidateBackend rejects it earlier with the same guidance).
func ResolveEngine(be string, _ config.Defaults) (string, error) {
	switch be {
	case "microvm":
		return resolveSmolvm()
	case "microsandbox":
		return resolveMsb()
	}
	return "", fmt.Errorf("unknown backend %q — use backend = \"microsandbox\" (or \"microvm\"); "+
		"the docker/podman container backend was removed", be)
}

// SweepEngines returns every runner whose sessions a sweep or report
// (clean, doctor, list) must cover: each microVM runner available on this
// host. The vm* sweeps filter by base dir and by runner, so a runner without
// sessions is a no-op.
func SweepEngines(_ config.Defaults) []string {
	var engines []string
	if hasExec(smolvmBin()) {
		engines = append(engines, smolvmEngine)
	}
	if hasExec(msbBin()) {
		engines = append(engines, msbEngine)
	}
	return engines
}

// resolveSmolvm confirms the microVM backend can run and returns its ENGINE
// IDENTITY, the constant "smolvm" — never the override path. That identity is
// what flows through RunOpts.Engine and every (engine, name) call, so the
// runner dispatch (engine == smolvmEngine) stays robust even when
// SANDBOXER_SMOLVM points the actual binary somewhere off PATH; the real binary
// is resolved separately at exec time (smolvmBin). smolvm is a preinstalled
// host requirement — an absent one is a clear error with an install hint,
// never an autodownload.
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

// resolveMsb confirms the microsandbox backend can run and returns its ENGINE
// IDENTITY, the constant "microsandbox" — never the override path, for the same
// reason resolveSmolvm returns its own constant. Like smolvm it is a
// preinstalled host requirement: an absent one is a clear error with an install
// hint, never an autodownload.
func resolveMsb() (string, error) {
	if hasExec(msbBin()) {
		return msbEngine, nil
	}
	return "", errors.New("the microsandbox backend needs msb on PATH " +
		"(install: https://microsandbox.dev — SANDBOXER_MSB overrides the path; " +
		"on NixOS use the flake's microsandbox package: the release binary is dynamically linked)")
}

// msbBin is the actual binary the microsandbox backend executes: the
// SANDBOXER_MSB override (a name or an absolute path), else "msb" resolved on
// PATH. Distinct from the engine identity msbEngine, which is only ever the
// constant "microsandbox".
func msbBin() string {
	if b := os.Getenv("SANDBOXER_MSB"); b != "" {
		return b
	}
	return "msb"
}

// MsbStatus reports the microsandbox backend's host readiness for `doctor`:
// whether the msb binary is found (via msbBin), its `--version` line when it
// runs, whether /dev/kvm exists on Linux, and whether MSB_HOME is short enough
// for a sandbox's agent-relay socket to fit in sun_path — the one microsandbox
// prerequisite that is invisible until the first `create` fails.
func MsbStatus() (present bool, version string, kvmOK, homeOK bool) {
	kvmOK = devKVMPresent()
	homeOK = msbHomeRoomy(msbHome())
	if !hasExec(msbBin()) {
		return false, "", kvmOK, homeOK
	}
	present = true
	if out, err := exec.Command(msbBin(), "--version").Output(); err == nil {
		version = strings.TrimSpace(string(out))
	}
	return present, version, kvmOK, homeOK
}

// MsbHome exposes the effective MSB_HOME for doctor's hint.
func MsbHome() string { return msbHome() }

func hasExec(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
