package cli

import (
	"io"

	"github.com/irasikhin/sandboxer/internal/config"
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
