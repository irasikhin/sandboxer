package backend

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/registry"
)

// authEnvFlags builds the --env args that pass each agent's auth-related
// environment (e.g. ANTHROPIC_API_KEY) through to the container when the user
// has set it on the host.
//
// It deliberately does NOT bind any host credential *directory*: each sandbox
// has its own isolated $HOME (sandbox.Base.HomeDir), so an agent authenticates
// inside its own sandbox (claude login, or one of these env vars) and nothing
// from the host's real config — tokens, project history, MCP servers — is ever
// pulled in. This also means parallel sandboxes never race on one shared
// ~/.claude.json. Passing an env var is an explicit host action by the user, not
// host state being mounted, so it stays opt-in here.
func authEnvFlags(authAgents []string) []string {
	var out []string
	for _, name := range authAgents {
		a, err := registry.Get(name)
		if err != nil {
			continue
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
	// Sorted keys: the argv is fingerprinted (ConfigHash) and shown (compose),
	// so map iteration order must not leak into it.
	keys := make([]string, 0, len(p.Env))
	for k := range p.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, "--env", k+"="+p.Env[k])
	}
	return out
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
