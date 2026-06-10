package config

import "testing"

// TestMergeProfileOverrides pins the field-by-field override semantics of
// mergeProfile: every field set on `over` wins, while a field left empty on
// `over` keeps the base value.
func TestMergeProfileOverrides(t *testing.T) {
	// given: a base with a couple of fields, and an over that sets every
	// otherwise-untested override field.
	base := Profile{
		Backend: "podman",
		Agent:   "claude",
		Network: Network{AllowedDomains: []string{"base.example"}},
	}
	egress := true
	over := Profile{
		Network:     Network{AllowedDomains: []string{"over.example"}},
		Proxy:       Proxy{HTTP: "http://p:1", HTTPS: "http://p:2", No: "localhost", Upstream: "http://up:3"},
		Agents:      []string{"codex"},
		Egress:      &egress,
		ExtraMounts: []Mount{{}},
		Setup:       "npm ci",
		Tools:       []string{"node"},
		MCP:         []string{"fetch"},
	}

	// when
	got := mergeProfile(base, over)

	// then: each override wins
	if len(got.Network.AllowedDomains) != 1 || got.Network.AllowedDomains[0] != "over.example" {
		t.Errorf("AllowedDomains = %v, want [over.example]", got.Network.AllowedDomains)
	}
	if got.Proxy != over.Proxy {
		t.Errorf("Proxy = %+v, want %+v", got.Proxy, over.Proxy)
	}
	if len(got.Agents) != 1 || got.Agents[0] != "codex" {
		t.Errorf("Agents = %v, want [codex]", got.Agents)
	}
	if got.Egress == nil || !*got.Egress {
		t.Errorf("Egress = %v, want overridden true", got.Egress)
	}
	if len(got.ExtraMounts) != 1 {
		t.Errorf("ExtraMounts len = %d, want 1", len(got.ExtraMounts))
	}
	// Setup/Tools/MCP must survive the merge — Select runs EVERY profile load
	// (flat files included) through it, so a dropped field here silently
	// disables the whole feature downstream (no setup run, no tool-pack image,
	// no MCP wiring).
	if got.Setup != "npm ci" {
		t.Errorf("Setup = %q, want npm ci", got.Setup)
	}
	if len(got.Tools) != 1 || got.Tools[0] != "node" {
		t.Errorf("Tools = %v, want [node]", got.Tools)
	}
	if len(got.MCP) != 1 || got.MCP[0] != "fetch" {
		t.Errorf("MCP = %v, want [fetch]", got.MCP)
	}
	// and: fields left empty on over keep the base value
	if got.Backend != "podman" || got.Agent != "claude" {
		t.Errorf("base fields lost: backend=%q agent=%q", got.Backend, got.Agent)
	}
}
