// Package registry is the single source of truth for the agent catalog.
//
// The data lives in agents/registry.json (embedded here, and also read by the
// Nix flake via builtins.fromJSON for the toolbox image). Each agent declares
// its binary name, the env vars that carry its credentials (passed through to
// the sandbox when set on the host), and the llm-agents package name used to
// bake it into the image.
package registry

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	_ "embed"
)

//go:embed registry.json
var registryJSON []byte

// Agent is one entry of the catalog.
type Agent struct {
	Bin        string   `json:"bin"`
	AuthEnv    []string `json:"authEnv"`
	NixPackage string   `json:"nixPackage"`
	// Image reports whether the agent is baked into the toolbox image. A nil
	// pointer means yes (default); only codex sets it false.
	Image *bool `json:"image,omitempty"`
	// Seed lists the agent's config locations in the HOST home — credentials,
	// settings, global memory — copied into a sandbox's private home when the
	// profile opts in with hostConfigs, so the agent starts already
	// authenticated. Paths are slash-relative to $HOME; an agent without seed
	// entries only authenticates via env or an in-sandbox login.
	Seed []SeedPath `json:"seed,omitempty"`
}

// SeedPath is one host-home location an agent's config is seeded from.
type SeedPath struct {
	// Path is the file or directory, slash-relative to the home dir.
	Path string `json:"path"`
	// Skip names subpaths (slash-relative to Path) excluded from the copy —
	// bulky, private or machine-bound data an agent works fine without
	// (transcripts, caches, session logs).
	Skip []string `json:"skip,omitempty"`
}

var catalog map[string]Agent

func init() {
	if err := json.Unmarshal(registryJSON, &catalog); err != nil {
		panic("registry: invalid embedded registry.json: " + err.Error())
	}
}

// Names returns the agent names, sorted.
func Names() []string {
	return slices.Sorted(maps.Keys(catalog))
}

// Get returns the agent with the given name.
func Get(name string) (Agent, error) {
	a, ok := catalog[name]
	if !ok {
		return Agent{}, fmt.Errorf("unknown agent: %s (see `sandboxer agents`)", name)
	}
	return a, nil
}
