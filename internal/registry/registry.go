// Package registry is the single source of truth for the agent catalog.
//
// The data lives in agents/registry.json (embedded here, and also read by the
// Nix flake via builtins.fromJSON for the toolbox image). Each agent declares
// how to launch it (command templates), whether it understands Claude Code's
// native /sandbox, which host config dirs/env carry its credentials, and the
// llm-agents package name used to bake it into the image.
package registry

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	_ "embed"
)

//go:embed registry.json
var registryJSON []byte

// AuthDir is a host config path to bind into the sandbox (HOME inside the
// container equals the host HOME, so the same path is used on both sides).
type AuthDir struct {
	Path     string `json:"path"`
	Mode     string `json:"mode"`
	Optional bool   `json:"optional,omitempty"`
}

// Agent is one entry of the catalog.
type Agent struct {
	Bin            string    `json:"bin"`
	Interactive    string    `json:"interactive"`
	Headless       string    `json:"headless"`
	NativeSandbox  bool      `json:"nativeSandbox"`
	AuthConfigDirs []AuthDir `json:"authConfigDirs"`
	AuthEnv        []string  `json:"authEnv"`
	NixPackage     string    `json:"nixPackage"`
	// Image reports whether the agent is baked into the toolbox image. A nil
	// pointer means yes (default); only codex sets it false.
	Image     *bool  `json:"image,omitempty"`
	ImageNote string `json:"imageNote,omitempty"`
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

// HeadlessCmd renders the agent's headless command as a shell string suitable
// for `bash -c` (native) or `bash -lc` inside the container.
func (a Agent) HeadlessCmd(model string, domains []string, task string) string {
	return a.render(a.Headless, model, domains, task)
}

// InteractiveCmd renders the agent's interactive command as a shell string.
func (a Agent) InteractiveCmd(model string, domains []string) string {
	return a.render(a.Interactive, model, domains, "")
}

func (a Agent) render(tmpl, model string, domains []string, task string) string {
	sflag := ""
	if a.NativeSandbox {
		sflag = "--settings " + shellQuote(SettingsJSON(domains))
	}
	mflag := ""
	if model != "" {
		mflag = "--model " + shellQuote(model)
	}
	tq := ""
	if task != "" {
		tq = shellQuote(task)
	}
	r := strings.NewReplacer(
		"{settingsFlag}", sflag,
		"{modelFlag}", mflag,
		"{task}", tq,
	)
	return strings.TrimSpace(r.Replace(tmpl))
}

// shellQuote single-quotes a string for POSIX shells (equivalent to bash
// `printf %q` for the cases we need): wrap in single quotes, and turn any
// embedded single quote into '\”.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// SettingsJSON builds the Claude Code `--settings` payload that enables the
// native sandbox with the given egress allowlist.
func SettingsJSON(domains []string) string {
	if domains == nil {
		domains = []string{}
	}
	payload := map[string]any{
		"sandbox": map[string]any{
			"enabled":                  true,
			"autoAllowBashIfSandboxed": true,
			"network": map[string]any{
				"allowedDomains": domains,
			},
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		// The payload is static-shaped; marshaling cannot fail in practice.
		return `{"sandbox":{"enabled":true}}`
	}
	return string(b)
}
