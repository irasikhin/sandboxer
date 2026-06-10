// Package toolbox builds the sandboxer toolbox container image without requiring
// nix on the host: it drives an ephemeral, public `nixos/nix` container (via the
// host's docker/podman) that realizes a minimal dockerTools image from a flake
// embedded in this package, then the host engine loads the resulting tarball.
//
// The embedded flake (assets/flake.nix) references only public inputs (nixpkgs,
// llm-agents); the sandboxer binary is injected by copying the running
// executable into the build context, so the build never needs the sandboxer
// repo or a local checkout.
package toolbox

import (
	"embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/registry"
)

//go:embed assets/flake.nix
var assets embed.FS

const (
	// NixImage is the public builder image (docker.io), pinned for reproducibility.
	NixImage = "docker.io/nixos/nix:2.31.2"
	// builtName is the image name:tag the embedded flake produces and loads.
	builtName = config.DefaultImage
	// cacheVolume persists the nix store across builds when --cache is set.
	cacheVolume = "sandboxer-nix-cache"
	// builderName is the deterministic name for the ephemeral builder container,
	// so a leftover from an aborted run can be force-removed before the next.
	builderName = "sandboxer-image-builder"
)

// BuildOpts configures a toolbox image build.
type BuildOpts struct {
	Engine      string // resolved container engine (podman|docker); required
	Image       string // final tag; retagged from builtName when different
	NixImage    string // builder image override; "" → NixImage
	Cache       bool   // keep a persistent nix-store volume for fast rebuilds
	KeepBuilder bool   // don't remove the nixos/nix image afterward
	// BuilderPulled marks the builder image as pulled by an earlier step of
	// this command (the "latest" pin resolver runs it before BuildImage and the
	// engine auto-pulls), so clean-by-default still removes it afterward even
	// though BuildImage's own probe now sees it as present.
	BuilderPulled bool
	Refresh       bool      // re-fetch flake inputs (nix build --refresh)
	ExtraArgs     []string  // extra engine `run` flags for the builder (escape hatch)
	Spec          Spec      // image variant customization (attrs, user nix, rev overrides); zero = stock
	Stdout        io.Writer // build chatter / engine stdout
	Stderr        io.Writer // progress banners / engine stderr
}

// BuildImage assembles the build context, runs the ephemeral nix builder, loads
// the resulting image into the host engine, retags it if a custom tag was asked
// for, and cleans up after itself (clean by default — see the cleanup steps).
func BuildImage(o BuildOpts) error {
	if o.Engine == "" {
		return errors.New("no container engine for build-image")
	}
	if o.NixImage == "" {
		o.NixImage = NixImage
	}
	if o.Image == "" {
		o.Image = builtName
	}
	progress := o.Stderr
	if progress == nil {
		progress = io.Discard
	}

	ctxDir, err := os.MkdirTemp("", "sandboxer-ctx-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(ctxDir) }()
	outDir, err := os.MkdirTemp("", "sandboxer-out-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(outDir) }()

	if err := writeContext(ctxDir, o.Spec); err != nil {
		return fmt.Errorf("assemble build context: %w", err)
	}

	// Track whether we pull the builder image, so cleanup only removes what we
	// added (never a nixos/nix the user already had). BuilderPulled folds in a
	// pull done by an earlier step of the same command (the pin resolver).
	pulledBuilder := o.BuilderPulled || !imageExists(o.Engine, o.NixImage)
	// Clear any leftover builder container from a previously aborted run.
	_ = exec.Command(o.Engine, "rm", "-f", builderName).Run()

	cacheVol := ""
	if o.Cache {
		cacheVol = cacheVolume
	}

	fmt.Fprintf(progress, "sandboxer: building toolbox image %q via %s + %s "+
		"(several minutes on first run)…\n", o.Image, o.Engine, o.NixImage)

	build := exec.Command(o.Engine, builderArgv(o, ctxDir, outDir, cacheVol)...)
	build.Stdout = o.Stdout
	build.Stderr = o.Stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("toolbox image build failed: %w", err)
	}

	// The flake always produces builtName:latest, and `engine load` re-points
	// that tag at the freshly built image. When the real target is a different
	// tag (a var- variant, a custom SANDBOXER_IMAGE), remember what the stock
	// tag pointed at so it can be given back after the retag — a variant build
	// must never leave the user's default image tagless (the next stock
	// enter/exec would then trigger a full rebuild).
	prevID := ""
	if o.Image != builtName {
		prevID = imageID(o.Engine, builtName)
	}

	tar := filepath.Join(outDir, "image.tar.gz")
	fmt.Fprintf(progress, "sandboxer: loading image into %s…\n", o.Engine)
	load := exec.Command(o.Engine, loadArgv(tar)...)
	load.Stdout = o.Stdout
	load.Stderr = o.Stderr
	if err := load.Run(); err != nil {
		return fmt.Errorf("image load failed: %w", err)
	}

	// Retag to the real target, then restore the stock tag to the image it
	// pointed at before the load (or drop it when there was none, so a custom
	// tag still leaves exactly one new image behind).
	if o.Image != builtName {
		if err := exec.Command(o.Engine, "tag", builtName, o.Image).Run(); err != nil {
			return fmt.Errorf("retag %s -> %s: %w", builtName, o.Image, err)
		}
		if prevID != "" {
			if err := exec.Command(o.Engine, "tag", prevID, builtName).Run(); err != nil {
				return fmt.Errorf("restore stock tag %s -> %s: %w", prevID, builtName, err)
			}
		} else {
			_ = exec.Command(o.Engine, "rmi", builtName).Run()
		}
		fmt.Fprintf(progress, "sandboxer: tagged image as %s\n", o.Image)
	}

	// Cleanup: drop the builder image only if we pulled it ourselves.
	if pulledBuilder && !o.KeepBuilder {
		fmt.Fprintf(progress, "sandboxer: removing builder image %s…\n", o.NixImage)
		_ = exec.Command(o.Engine, "rmi", o.NixImage).Run()
	}

	fmt.Fprintf(progress, "sandboxer: done — image %s is ready.\n", o.Image)
	return nil
}

// stubUserNix is the user.nix written when the profile has no nix hook: the
// flake's import is unconditional, so the stub keeps a stock build identical
// to one before the hook existed (no customization in either phase).
const stubUserNix = "{ pkgs }: { }\n"

// writeContext populates the flake build context: the embedded flake.nix, the
// generated agents.nix/tools.nix lists, the user.nix image hook, and a copy of
// the running sandboxer binary (so the flake can inject it as ./sandboxer
// without rebuilding from source).
func writeContext(ctxDir string, spec Spec) error {
	flake, err := assets.ReadFile("assets/flake.nix")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(ctxDir, "flake.nix"), flake, 0o644); err != nil {
		return err
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
	// user.nix is the profile's image hook — copied verbatim when set, the
	// no-op stub otherwise — so the flake imports it unconditionally.
	userNix := filepath.Join(ctxDir, "user.nix")
	if spec.NixFile != "" {
		if err := copyFile(spec.NixFile, userNix); err != nil {
			return fmt.Errorf("copy image.nix: %w", err)
		}
	} else if err := os.WriteFile(userNix, []byte(stubUserNix), 0o644); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate sandboxer binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return copyFile(exe, filepath.Join(ctxDir, "sandboxer"))
}

// builderArgv builds the host engine `run` argv for the ephemeral nix builder.
// Pure: no exec, env or filesystem — asserted directly in tests.
func builderArgv(o BuildOpts, ctxDir, outDir, cacheVol string) []string {
	args := []string{
		"run", "--rm", "--name", builderName,
		"--volume", ctxDir + ":/src:ro",
		"--volume", outDir + ":/out:rw",
	}
	if cacheVol != "" {
		args = append(args, "--volume", cacheVol+":/nix")
	}
	args = append(args, o.ExtraArgs...)
	args = append(args, o.NixImage, "sh", "-lc",
		builderScript(o.Refresh, o.Spec.NixpkgsRev, o.Spec.LLMAgentsRev))
	return args
}

// builderScript is the in-container shell run by the nix builder: build the
// `#image` derivation from the mounted /src flake and copy the realized image
// tarball to the bind-mounted /out. `--accept-flake-config` lets the
// llm-agents binary cache substituter be used (no agent compiles from source);
// `--no-write-lock-file` keeps the read-only /src untouched. A rev override
// becomes an --override-input flag only when it differs from the embedded pin
// — when the effective rev IS the pin the script stays byte-identical to a
// stock build, so nix's eval cache is shared. Revs are full 40-hex commits
// (ValidateImageSpec / ResolveLatest enforce it), so the != comparison is
// exact and a github: flakeref override never needs a GitHub API resolve.
func builderScript(refresh bool, nixpkgsRev, llmAgentsRev string) string {
	flags := ""
	if refresh {
		flags += "--refresh "
	}
	embNixpkgs, embLLMAgents := EmbeddedRevs()
	if nixpkgsRev != "" && nixpkgsRev != embNixpkgs {
		flags += "--override-input nixpkgs github:NixOS/nixpkgs/" + nixpkgsRev + " "
	}
	if llmAgentsRev != "" && llmAgentsRev != embLLMAgents {
		flags += "--override-input llm-agents github:numtide/llm-agents.nix/" + llmAgentsRev + " "
	}
	return "set -e; " +
		"nix --extra-experimental-features 'nix-command flakes' " +
		"--accept-flake-config build " + flags +
		"--no-write-lock-file --no-link --print-out-paths " +
		"path:/src#image > /out/storepath && " +
		`cp -L "$(cat /out/storepath)" /out/image.tar.gz`
}

// loadArgv is the engine `load` argv (engine binary supplied by the caller).
func loadArgv(tarPath string) []string {
	return []string{"load", "-i", tarPath}
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

// imageAgentPackages returns the llm-agents package names for agents baked into
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

// imageExists reports whether the engine has the image locally. Duplicated from
// backend.ImageExists to keep toolbox free of a backend import (backend imports
// toolbox for the auto-build path).
func imageExists(engine, image string) bool {
	return exec.Command(engine, "image", "inspect", image).Run() == nil
}

// imageID returns the engine's local ID for image ("" when absent or on any
// failure) — backend.ImageID's twin, duplicated for the same no-import-cycle
// reason as imageExists.
func imageID(engine, image string) string {
	out, err := exec.Command(engine, "image", "inspect", "--format", "{{.Id}}", image).Output()
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSpace(string(out)), "sha256:")
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
