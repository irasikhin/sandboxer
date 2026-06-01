package config

import (
	"slices"
	"testing"

	"github.com/irasikhin/sandboxer/internal/registry"
)

func TestLoadDefaultsFromEnv(t *testing.T) {
	t.Setenv("SANDBOXER_MODEL", "m")
	t.Setenv("SANDBOXER_AGENT", "crush")
	t.Setenv("SANDBOXER_BACKEND", "docker")
	t.Setenv("SANDBOXER_DOMAINS", "a.com")
	t.Setenv("SANDBOXER_IMAGE", "img:1")
	t.Setenv("SANDBOXER_ENGINE", "docker")
	t.Setenv("SANDBOXER_MAX_PARALLEL", "9")
	t.Setenv("SANDBOXER_NICE", "3")
	t.Setenv("SANDBOXER_MEM", "2G")
	t.Setenv("SANDBOXER_CPU", "50%")
	t.Setenv("SANDBOXER_WALL", "60")

	d := LoadDefaults()
	if d.Model != "m" || d.Agent != "crush" || d.Backend != "docker" || d.Domains != "a.com" ||
		d.Image != "img:1" || d.Engine != "docker" || d.MaxParallel != 9 || d.Nice != 3 ||
		d.Mem != "2G" || d.CPU != "50%" || d.Wall != "60" {
		t.Errorf("LoadDefaults from env = %+v", d)
	}
}

func TestLoadDefaultsBareAndBadInt(t *testing.T) {
	for _, k := range []string{
		"SANDBOXER_MODEL", "SANDBOXER_AGENT", "SANDBOXER_BACKEND", "SANDBOXER_DOMAINS",
		"SANDBOXER_IMAGE", "SANDBOXER_ENGINE", "SANDBOXER_MEM", "SANDBOXER_CPU", "SANDBOXER_WALL",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("SANDBOXER_MAX_PARALLEL", "not-an-int") // falls back to default
	t.Setenv("SANDBOXER_NICE", "")

	d := LoadDefaults()
	if d.Agent != "claude" || d.Backend != "podman" || d.Domains != DefaultDomains || d.Image != DefaultImage {
		t.Errorf("bare defaults = %+v", d)
	}
	if d.MaxParallel != 4 || d.Nice != 10 {
		t.Errorf("MaxParallel=%d Nice=%d, want 4/10", d.MaxParallel, d.Nice)
	}
}

func TestDomainsCSV(t *testing.T) {
	if got := (Runtime{Domains: []string{"a", "b"}}).DomainsCSV(); got != "a,b" {
		t.Errorf("DomainsCSV = %q", got)
	}
	if got := (Runtime{}).DomainsCSV(); got != "" {
		t.Errorf("empty DomainsCSV = %q", got)
	}
}

func TestResolveRuntimeAuthAgents(t *testing.T) {
	// Explicit agents list is carried verbatim.
	p := &Profile{Agents: []string{"claude", "codex"}}
	rt := ResolveRuntime(p, Defaults{}, "", "", Overrides{})
	if !slices.Equal(rt.AuthAgents, []string{"claude", "codex"}) {
		t.Errorf("AuthAgents = %v", rt.AuthAgents)
	}
	// No agents list → the full registry.
	rt2 := ResolveRuntime(&Profile{}, Defaults{}, "", "", Overrides{})
	if !slices.Equal(rt2.AuthAgents, registry.Names()) {
		t.Errorf("AuthAgents fallback = %v, want registry.Names()", rt2.AuthAgents)
	}
}

func TestResolveRuntimeDomainsPrecedence(t *testing.T) {
	// Flag CSV wins and is trimmed/split.
	rt := ResolveRuntime(&Profile{Network: Network{AllowedDomains: []string{"p.com"}}},
		Defaults{}, "base.com", "", Overrides{Domains: "a.com, , b.com"})
	if !slices.Equal(rt.Domains, []string{"a.com", "b.com"}) {
		t.Errorf("flag domains = %v", rt.Domains)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load("/no/such/profile.yaml"); err == nil {
		t.Error("Load of missing file should error")
	}
}
