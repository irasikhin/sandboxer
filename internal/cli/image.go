package cli

import (
	"io"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/toolbox"
)

// resolveImage picks the toolbox image for a profile. Without image
// customization (`tools:` / `image:`) it is the configured default image; with
// any it is the spec's content-addressed variant tag (built on demand by the
// backend, shared across identical customizations). A variant's tracking input
// revs (the "" / "latest" default) are pinned to concrete commits first — a
// stamped-cache hit, or a one-time resolve on a miss. Resolving runs host `git
// ls-remote` (git is a hard requirement; there is no engine in this path), so a
// cold cache resolves on a container-less host exactly as on one with
// docker/podman; only `image build` moves a warm stamp. The resolve prints a
// progress line, so its output goes to stderr — interactive callers pass the
// command's stderr, the best-effort show probe stays quiet. It returns the
// image reference and the pinned spec the backend needs to build a missing
// variant.
func resolveImage(prof *config.Profile, stderr io.Writer) (string, toolbox.Spec, error) {
	spec, err := toolbox.ResolveSpec(prof)
	if err != nil {
		return "", toolbox.Spec{}, err
	}
	if spec.Empty() {
		return config.LoadDefaults().Image, spec, nil
	}
	spec, err = toolbox.PinSpec(spec, false, stderr)
	if err != nil {
		return "", toolbox.Spec{}, err
	}
	return spec.Tag(), spec, nil
}
