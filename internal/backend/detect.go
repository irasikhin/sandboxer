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
//     that binary is installed — so --backend docker actually runs docker even
//     when podman is also present;
//   - otherwise auto-detect, preferring podman then docker.
//
// A requested-but-missing engine is not an error: we fall back to whatever is
// installed so the default backend ("podman") still works on a docker-only host.
// Callers display the engine we return (see EngineLabel), so the banner never
// disagrees with what actually runs.
func ResolveEngine(be string, d config.Defaults) (string, error) {
	if d.Engine != "" {
		return d.Engine, nil
	}
	if (be == "podman" || be == "docker") && hasExec(be) {
		return be, nil
	}
	if hasExec("podman") {
		return "podman", nil
	}
	if hasExec("docker") {
		return "docker", nil
	}
	return "", errors.New("need podman or docker " +
		"(NixOS: virtualisation.podman.enable)")
}

// DetectEngine resolves the engine with no explicit backend preference (the
// plain podman→docker auto-detect).
func DetectEngine(d config.Defaults) (string, error) { return ResolveEngine("", d) }

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
