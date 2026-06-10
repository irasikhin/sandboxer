package cli

import (
	"fmt"
	"io"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/registry"
	"github.com/irasikhin/sandboxer/internal/toolbox"
)

// resolveImage picks the toolbox image for a profile. Without a `tools:` pack it
// is the default image; with one it is a per-profile variant baked with the
// resolved nixpkgs attrs (built on demand by the backend, cached by tool-set
// hash). It returns the image reference and the nixpkgs attrs to hand the
// backend so a missing variant can be built.
func resolveImage(prof *config.Profile) (image string, tools []string, err error) {
	if prof == nil || len(prof.Tools) == 0 {
		return config.LoadDefaults().Image, nil, nil
	}
	attrs, err := registry.ResolveTools(prof.Tools)
	if err != nil {
		return "", nil, err
	}
	return toolbox.ToolsImageTag(attrs), attrs, nil
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
