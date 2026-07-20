package registry

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	_ "embed"
)

// The tool-pack catalog: a curated map of a friendly pack name (used in a
// profile's `tools:`) to the nixpkgs attribute(s) baked into a per-profile
// toolbox image variant. Embedded here and consumed by the flake the same way
// as registry.json — edit the JSON, never duplicate it.
//
//go:embed tools.json
var toolsJSON []byte

var toolCatalog map[string][]string

func init() {
	if err := json.Unmarshal(toolsJSON, &toolCatalog); err != nil {
		panic("registry: invalid embedded tools.json: " + err.Error())
	}
}

// ToolNames returns the available tool-pack names, sorted.
func ToolNames() []string {
	return slices.Sorted(maps.Keys(toolCatalog))
}

// ResolveTools maps a profile's friendly tool-pack names to the sorted, unique
// set of nixpkgs attributes to bake into the image. An unknown pack is an error
// (listing what is available) rather than a silently empty image. An empty
// input resolves to no packages.
func ResolveTools(names []string) ([]string, error) {
	seen := map[string]bool{}
	var attrs []string
	for _, n := range names {
		pkgs, ok := toolCatalog[n]
		if !ok {
			return nil, fmt.Errorf("unknown tool pack: %s (have: %v)", n, ToolNames())
		}
		for _, p := range pkgs {
			if !seen[p] {
				seen[p] = true
				attrs = append(attrs, p)
			}
		}
	}
	slices.Sort(attrs)
	return attrs, nil
}
