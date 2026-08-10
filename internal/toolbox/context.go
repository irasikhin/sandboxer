// Package toolbox assembles the build context for the sandboxer toolbox image
// and builds it with host nix (BuildImageHostNix).
//
// The embedded flake (assets/flake.nix) references one public input (nixpkgs)
// and is written into the build context together with the generated
// agents.nix/tools.nix/overlay.nix, the files/env JSON and the vendored pi
// package, so the build never needs the sandboxer repo or a local checkout.
// The sandboxer binary is NOT part of the image — it is a host tool (see
// writeContext).
package toolbox

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/irasikhin/sandboxer/internal/registry"
)

// images.nix carries what is IN the image; flake.nix only resolves the
// profile's context and imports it. Both are embedded because the build
// context sees nothing but the dir we render — the repo is never read. The
// root flake imports the same images.nix, so the image a user gets and the
// image CI builds cannot drift apart again.
//
//go:embed assets/flake.nix assets/images.nix assets/pi
var assets embed.FS

// stubOverlay is the overlay.nix written when the profile has none: the
// flake's import is unconditional, and a no-op overlay keeps a stock build
// identical to one without any customization.
const stubOverlay = "final: prev: { }\n"

// writeContext populates the flake build context: the embedded flake.nix and
// the images.nix it imports, the generated agents.nix/tools.nix lists, the
// profile's overlay.nix (a plain nixpkgs overlay; no-op stub when unset) and
// its files.json/env.json static customization.
func writeContext(ctxDir string, spec Spec) error {
	// os.Mkdir, not MkdirAll: a missing ctxDir must stay an error (the caller
	// owns creating the context), never be silently conjured up.
	if err := os.Mkdir(filepath.Join(ctxDir, "pi"), 0o755); err != nil {
		return err
	}
	for _, name := range []string{"flake.nix", "images.nix", "pi/package.nix", "pi/package-lock.json"} {
		data, err := assets.ReadFile("assets/" + name)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(ctxDir, filepath.FromSlash(name)), data, 0o644); err != nil {
			return err
		}
	}
	agents := renderNixList(imageAgentPackages())
	if err := os.WriteFile(filepath.Join(ctxDir, "agents.nix"), []byte(agents), 0o644); err != nil {
		return err
	}
	// tools.nix carries the spec's nixpkgs attrs (tools packs + extraPkgs;
	// empty list for the default image); the flake imports it and bakes the
	// named attrs into the image.
	if err := os.WriteFile(filepath.Join(ctxDir, "tools.nix"), []byte(renderNixList(spec.Attrs)), 0o644); err != nil {
		return err
	}
	// overlay.nix is the profile's plain nixpkgs overlay — copied verbatim
	// when set, the no-op stub otherwise — so the flake imports it
	// unconditionally.
	overlayNix := filepath.Join(ctxDir, "overlay.nix")
	if spec.OverlayFile != "" {
		if err := copyFile(spec.OverlayFile, overlayNix); err != nil {
			return fmt.Errorf("copy image.overlay: %w", err)
		}
	} else if err := os.WriteFile(overlayNix, []byte(stubOverlay), 0o644); err != nil {
		return err
	}
	// files.json / env.json carry the profile's static customization as JSON
	// (trivially escaped on both sides; the flake reads them with
	// builtins.fromJSON). Always written so the flake's import is
	// unconditional.
	for name, m := range map[string]map[string]string{"files.json": spec.Files, "env.json": spec.Env} {
		data, err := json.Marshal(orEmpty(m))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(ctxDir, name), data, 0o644); err != nil {
			return err
		}
	}
	// The sandboxer binary is NOT copied into the image — it is a host tool.
	return nil
}

// orEmpty keeps the rendered JSON an object ({} not null) for a nil map.
func orEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// renderNixList renders a set of package names as a nix list literal (sorted for
// determinism), consumed by assets/flake.nix as ./agents.nix and ./tools.nix.
func renderNixList(names []string) string {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	var b strings.Builder
	b.WriteString("[\n")
	for _, n := range sorted {
		fmt.Fprintf(&b, "  %q\n", n)
	}
	b.WriteString("]\n")
	return b.String()
}

// imageAgentPackages returns the nixpkgs package attrs for agents baked into
// the image (registry entries with "image" != false and a non-empty nixPackage).
func imageAgentPackages() []string {
	var out []string
	for _, name := range registry.Names() {
		a, err := registry.Get(name)
		if err != nil {
			continue
		}
		if a.Image != nil && !*a.Image {
			continue
		}
		if a.NixPackage == "" {
			continue
		}
		out = append(out, a.NixPackage)
	}
	return out
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
