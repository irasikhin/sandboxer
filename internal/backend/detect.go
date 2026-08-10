// Package backend runs an agent inside a microVM built from the toolbox
// image — a real machine per sandbox on libkrun (microsandbox), with a
// per-sandbox isolated home, source shares and the egress allowlist.
package backend

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/irasikhin/sandboxer/internal/config"
)

// ResolveEngine picks the runner a backend name resolves to: "microsandbox" →
// msb, the only backend. Anything else is an error — the docker/podman
// container backend and the smolvm "microvm" backend were both removed, so a
// legacy backend name gets the migration hint instead of a silent fallback
// (config.ValidateBackend rejects it earlier with the same guidance).
func ResolveEngine(be string, _ config.Defaults) (string, error) {
	switch be {
	case "microsandbox":
		return resolveMsb()
	case "microvm":
		return "", errors.New("the smolvm microvm backend was removed — set backend = \"microsandbox\"; " +
			"it is the same libkrun isolation with a name-bound egress allowlist")
	}
	return "", fmt.Errorf("unknown backend %q — use backend = \"microsandbox\"; "+
		"the docker/podman container backend was removed", be)
}

// SweepEngines returns every runner whose sessions a sweep or report
// (clean, doctor, list) must cover: the microsandbox runner when it is
// available on this host. The vm* sweeps filter by base dir, so a runner
// without sessions is a no-op.
func SweepEngines(_ config.Defaults) []string {
	if hasExec(msbBin()) {
		return []string{msbEngine}
	}
	return nil
}

// resolveMsb confirms the microsandbox backend can run and returns its ENGINE
// IDENTITY, the constant "microsandbox" — never the override path. That
// identity is what flows through RunOpts.Engine and every (engine, name) call,
// so records and labels stay robust even when SANDBOXER_MSB points the actual
// binary somewhere off PATH; the real binary is resolved separately at exec
// time (msbBin). msb is a preinstalled host requirement: an absent one is a
// clear error with an install hint, never an autodownload.
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
// runs, whether /dev/kvm exists on Linux (always true off Linux, where the
// platform hypervisor is used instead), and whether MSB_HOME is short enough
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
