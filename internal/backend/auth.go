package backend

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/registry"
)

// authFlags builds the --volume/--env args that bind each agent's credentials
// into the container. With ephemeral=true (headless runs), config dirs are
// copied into ephDir and mounted read-write from there, so a run can't mutate
// the real host credentials.
func authFlags(authAgents []string, ephemeral bool, ephDir string) []string {
	home, _ := os.UserHomeDir()
	var out []string
	for _, name := range authAgents {
		a, err := registry.Get(name)
		if err != nil {
			continue
		}
		for _, dir := range a.AuthConfigDirs {
			p := expandHome(dir.Path, home)
			if !pathExists(p) {
				continue
			}
			mode := dir.Mode
			if ephemeral {
				_ = os.MkdirAll(ephDir, 0o700)
				// cp -a preserves perms/symlinks, matching the bash.
				_ = exec.Command("cp", "-a", p, ephDir+"/").Run()
				p = filepath.Join(ephDir, filepath.Base(p))
				mode = "rw"
			}
			out = append(out, "--volume", fmt.Sprintf("%s:%s:%s", p, p, mode))
		}
		for _, e := range a.AuthEnv {
			if v := os.Getenv(e); v != "" {
				out = append(out, "--env", e+"="+v)
			}
		}
	}
	return out
}

// originMounts binds the origins of vendored dependencies back into the
// container (rw → writable for in-container push, ro → read-only), read from
// the sandbox manifest.
func originMounts(manifestPath string) []string {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil
	}
	var entries []struct {
		Origin string `json:"origin"`
		Mode   string `json:"mode"`
	}
	if json.Unmarshal(data, &entries) != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.Origin == "" || !pathExists(e.Origin) {
			continue
		}
		mode := "ro"
		if e.Mode == "rw" {
			mode = "rw"
		}
		out = append(out, "--volume", fmt.Sprintf("%s:%s:%s", e.Origin, e.Origin, mode))
	}
	return out
}

// extraMountsAndEnv adds the profile's extraMounts and env injections.
func extraMountsAndEnv(p *config.Profile) []string {
	if p == nil {
		return nil
	}
	var out []string
	for _, m := range p.ExtraMounts {
		mode := m.Mode
		if mode == "" {
			mode = "rw"
		}
		out = append(out, "--volume", fmt.Sprintf("%s:%s:%s", m.Source, m.Target, mode))
	}
	for k, v := range p.Env {
		out = append(out, "--env", k+"="+v)
	}
	return out
}

func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
