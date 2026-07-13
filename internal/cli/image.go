package cli

import (
	"io"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/registry"
	"github.com/irasikhin/sandboxer/internal/toolbox"
)

// resolveImage picks the toolbox image for a profile. Without image
// customization (`tools:` / `image:`) it is the configured default image; with
// any it is the spec's content-addressed variant tag (built on demand by the
// backend, shared across identical customizations). A "latest" input rev is
// pinned to a concrete commit first — a stamped-cache hit, or a one-time
// resolve via the engine on a miss ("" = no engine: a warm cache still works,
// a cold one fails with build-image guidance). The resolve runs a container
// (possibly pulling the nixos/nix builder image), so its banner and progress
// go to stderr — interactive callers pass the command's stderr, the
// best-effort show probe stays quiet. It returns the image reference and the
// pinned spec the backend needs to build a missing variant.
func resolveImage(prof *config.Profile, engine string, stderr io.Writer) (string, toolbox.Spec, error) {
	spec, err := toolbox.ResolveSpec(prof)
	if err != nil {
		return "", toolbox.Spec{}, err
	}
	if spec.Empty() {
		return config.LoadDefaults().Image, spec, nil
	}
	spec, err = toolbox.PinSpec(spec, engine, "", false, stderr)
	if err != nil {
		return "", toolbox.Spec{}, err
	}
	return spec.Tag(), spec, nil
}

// applyMCP wires a profile's `mcp:` servers into the run: it seeds the
// sandbox-home config (claude's ~/.claude.json format) and folds each server's
// domains into the egress allowlist (rt is mutated in place).
func applyMCP(t *target, rt *config.Runtime, _ io.Writer) error {
	if t.profile == nil || len(t.profile.MCP) == 0 {
		return nil
	}
	domains, err := registry.ApplyMCP(t.profile.MCP, t.base.HomeDir(t.slug))
	if err != nil {
		return err
	}
	rt.Domains = mergeDomains(rt.Domains, domains)
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
