package cli

import (
	"fmt"
	"io"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/registry"
	"github.com/irasikhin/sandboxer/internal/toolbox"
)

// resolveImage picks the toolbox image for a profile. Without image
// customization (`tools:` / `image:`) it is the configured default image; with
// any it is the spec's content-addressed variant tag (built on demand by the
// backend, shared across identical customizations). It returns the image
// reference and the resolved spec the backend needs to build a missing
// variant.
func resolveImage(prof *config.Profile) (string, toolbox.Spec, error) {
	spec, err := toolbox.ResolveSpec(prof)
	if err != nil {
		return "", toolbox.Spec{}, err
	}
	if spec.Empty() {
		return config.LoadDefaults().Image, spec, nil
	}
	return spec.Tag(), spec, nil
}

// applyMCP wires a profile's `mcp:` servers into the run: it seeds the agent's
// sandbox-home config and folds each server's domains into the egress allowlist
// (rt is mutated in place). For an agent whose config format is not yet
// supported it still allows the domains and notes that in-agent setup is needed.
func applyMCP(t *target, rt *config.Runtime, errOut io.Writer) error {
	if t.profile == nil || len(t.profile.MCP) == 0 {
		return nil
	}
	domains, seeded, err := registry.ApplyMCP(t.profile.MCP, rt.Agent, t.base.HomeDir(t.slug))
	if err != nil {
		return err
	}
	rt.Domains = mergeDomains(rt.Domains, domains)
	if !seeded {
		fmt.Fprintf(errOut, "sandboxer: note: MCP config seeding not yet supported for agent %q; "+
			"its domains are allowed — configure the server in-agent\n", rt.Agent)
	}
	return nil
}

// mcpAllowDomains returns the allowlist with the profile's MCP-server domains
// folded in — the pure half of applyMCP (no config seeding), for commands that
// must hash or print the same domain set enter/exec actually run with (show's
// session freshness verdict, compose's printed argv). The two must never
// disagree: both resolve through registry.ResolveMCP.
func mcpAllowDomains(prof *config.Profile, have []string) ([]string, error) {
	if prof == nil || len(prof.MCP) == 0 {
		return have, nil
	}
	_, domains, err := registry.ResolveMCP(prof.MCP)
	if err != nil {
		return nil, err
	}
	return mergeDomains(have, domains), nil
}

// mergeDomains appends add to have without introducing duplicates.
func mergeDomains(have, add []string) []string {
	seen := make(map[string]bool, len(have))
	for _, d := range have {
		seen[d] = true
	}
	for _, d := range add {
		if !seen[d] {
			seen[d] = true
			have = append(have, d)
		}
	}
	return have
}
