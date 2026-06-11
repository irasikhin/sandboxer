// Package backend runs an agent inside a podman/docker container built from the
// toolbox image, with a per-sandbox isolated home, dependency origin mounts and
// the egress allowlist.
package backend

import (
	"errors"
	"os/exec"

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

func hasExec(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
