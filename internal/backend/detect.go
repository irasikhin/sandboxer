// Package backend runs an agent inside an isolation backend: the native Claude
// Code /sandbox, or a podman/docker container built from the toolbox image
// (with credential bind-mounts, dependency origin mounts and the egress
// allowlist).
package backend

import (
	"errors"
	"os/exec"

	"github.com/irasikhin/sandboxer/internal/config"
)

// DetectEngine picks the container engine: SANDBOXER_ENGINE if set, else podman,
// else docker.
func DetectEngine(d config.Defaults) (string, error) {
	if d.Engine != "" {
		return d.Engine, nil
	}
	if hasExec("podman") {
		return "podman", nil
	}
	if hasExec("docker") {
		return "docker", nil
	}
	return "", errors.New("need podman or docker for the container backend " +
		"(NixOS: virtualisation.podman.enable), or use backend=native (claude only)")
}

func hasExec(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
