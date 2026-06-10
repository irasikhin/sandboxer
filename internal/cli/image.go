package cli

import (
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
