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
	"sort"

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
}

var catalog map[string]Agent

func init() {
	if err := json.Unmarshal(registryJSON, &catalog); err != nil {
		panic("registry: invalid embedded registry.json: " + err.Error())
	}
}

// Names returns the agent names, sorted.
func Names() []string {
	names := make([]string, 0, len(catalog))
	for n := range catalog {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Get returns the agent with the given name.
func Get(name string) (Agent, error) {
	a, ok := catalog[name]
	if !ok {
		return Agent{}, fmt.Errorf("unknown agent: %s (see `sandboxer agents`)", name)
	}
	return a, nil
}
