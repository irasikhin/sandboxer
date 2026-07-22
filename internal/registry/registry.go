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
	// Resume is the argv that relaunches the agent's previous conversation in a
	// restored pane (claude → ["claude","--continue"]). Empty = no known resume
	// command; the pane restores as a plain shell. An argv, not a shell string:
	// the backend renders it into the tmux restore script and owns the quoting,
	// so the registry stays declarative data. The Nix flake ignores this field.
	Resume []string `json:"resume,omitempty"`
	// ResumePick is the argv that opens the agent's interactive conversation
	// picker (claude → ["claude","--resume"]). Used instead of Resume when the
	// capture holds SEVERAL panes of this agent in the SAME directory: their
	// conversations cannot be told apart from outside the processes (verified:
	// claude neither keeps its transcript fd open nor exports a session id), and
	// Resume would open the same latest conversation in every one of them. The
	// picker lists exactly that directory's conversations — one keystroke each.
	ResumePick []string `json:"resumePick,omitempty"`
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

// Bins maps each agent's binary name to its catalog name — the lookup the
// session-restore capture uses to recognize an agent process in a tmux pane.
func Bins() map[string]string {
	out := make(map[string]string, len(catalog))
	for name, a := range catalog {
		out[a.Bin] = name
	}
	return out
}

// ResumeSpec is one agent's resume surface: Last relaunches the latest
// conversation of the pane's directory, Pick opens the interactive picker for
// panes whose conversation is ambiguous (several panes of the same agent in
// the same directory). The backend chooses per pane.
type ResumeSpec struct {
	Last []string
	Pick []string
}

// ResumeMap returns agent name → resume spec for every agent that declares
// one — the catalog projection the session restore feeds into the backend's
// TmuxRestoreScript.
func ResumeMap() map[string]ResumeSpec {
	out := map[string]ResumeSpec{}
	for name, a := range catalog {
		if len(a.Resume) > 0 || len(a.ResumePick) > 0 {
			out[name] = ResumeSpec{Last: a.Resume, Pick: a.ResumePick}
		}
	}
	return out
}
