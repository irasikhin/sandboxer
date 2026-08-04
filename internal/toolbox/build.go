// Package toolbox builds the sandboxer toolbox container image without requiring
// nix on the host: it drives an ephemeral, public `nixos/nix` container (via the
// host's docker/podman) that realizes a minimal dockerTools image from a flake
// embedded in this package, then the host engine loads the resulting tarball.
//
// The embedded flake (assets/flake.nix) references only public inputs (nixpkgs,
// llm-agents) and is written into the build context together with the
// generated agents.nix/tools.nix/overlay.nix and the files/env JSON, so the
// build never needs the sandboxer repo or a local checkout. The sandboxer
// binary is NOT part of the image — it is a host tool (see writeContext).
package toolbox

import (
	"embed"
	"encoding/json"
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

// images.nix carries what is IN the images; flake.nix only resolves the
// profile's context and imports it. Both are embedded because the builder
// container sees nothing but the context dir we render — the repo is never
// mounted. The root flake imports the same images.nix, so the image a user
// gets and the image CI builds cannot drift apart again.
//
//go:embed assets/flake.nix assets/images.nix
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
	// this command (the pin resolver runs it before BuildImage and the
	// engine auto-pulls), so clean-by-default still removes it afterward even
	// though BuildImage's own probe now sees it as present.
	BuilderPulled bool
	// DestTar, when set, copies the built image tarball there and SKIPS the
	// engine load / retag / proxy-image steps. It is how the microVM backend
	// gets a toolbox tar for its own store using a container engine to build
	// (reliable) instead of the in-VM builder (which a smolvm registry-pull bug
	// on nix-store images currently breaks). The proxy image is not built.
	DestTar   string
	ExtraArgs []string  // extra engine `run` flags for the builder (escape hatch)
	Spec      Spec      // image variant customization (attrs, user nix, rev overrides); zero = stock
	Stdout    io.Writer // build chatter / engine stdout
	Stderr    io.Writer // progress banners / engine stderr
}

// BuildImage assembles the build context, runs the ephemeral nix builder, loads
// the resulting image into the host engine, retags it if a custom tag was asked
// for, and cleans up after itself (clean by default — see the cleanup steps).
func BuildImage(o BuildOpts) error {
	if o.Engine == "" {
		return errors.New("no container engine for image build")
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

	// Track whether we pull the builder image, so cleanup only removes what we
	// added (never a nixos/nix the user already had). BuilderPulled folds in a
	// pull done by an earlier step of the same command; the probe runs BEFORE
	// the pin resolution below, whose resolver container may itself pull the
	// builder.
	pulledBuilder := o.BuilderPulled || !imageExists(o.Engine, o.NixImage)

	// Resolve tracking input revs ("" / "latest" — the default) to concrete
	// commits so every build, including the enter-time auto-build of a missing
	// stock image, bakes the stamped agent set — never a silently stale pin.
	// A spec the caller already pinned (image build does, for the tag) passes
	// through untouched.
	spec, err := PinSpec(o.Spec, o.Engine, o.NixImage, false, progress)
	if err != nil {
		return err
	}
	o.Spec = spec

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

	// microVM store path: take the built tar and stop — no engine load/retag,
	// no proxy image. The caller (backend.vmBuildImageToStore) moves it into the
	// microVM image store.
	if o.DestTar != "" {
		if err := copyFile(filepath.Join(outDir, "image.tar.gz"), o.DestTar); err != nil {
			return fmt.Errorf("copy built image to %s: %w", o.DestTar, err)
		}
		if pulledBuilder && !o.KeepBuilder {
			_ = exec.Command(o.Engine, "rmi", o.NixImage).Run()
		}
		return nil
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

	// Load the egress squid proxy image (stock name:tag, no retag) — built
	// alongside the toolbox image so a single `image build` readies both.
	proxyTar := filepath.Join(outDir, "proxy.tar.gz")
	fmt.Fprintf(progress, "sandboxer: loading egress proxy image into %s…\n", o.Engine)
	loadProxy := exec.Command(o.Engine, loadArgv(proxyTar)...)
	loadProxy.Stdout = o.Stdout
	loadProxy.Stderr = o.Stderr
	if err := loadProxy.Run(); err != nil {
		return fmt.Errorf("proxy image load failed: %w", err)
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

// stubOverlay is the overlay.nix written when the profile has none: the
// flake's import is unconditional, and a no-op overlay keeps a stock build
// identical to one without any customization.
const stubOverlay = "final: prev: { }\n"

// writeContext populates the flake build context: the embedded flake.nix and
// the images.nix it imports, the generated agents.nix/tools.nix lists, the
// profile's overlay.nix (a plain nixpkgs overlay; no-op stub when unset) and
// its files.json/env.json static customization.
func writeContext(ctxDir string, spec Spec) error {
	for _, name := range []string{"flake.nix", "images.nix"} {
		data, err := assets.ReadFile("assets/" + name)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(ctxDir, name), data, 0o644); err != nil {
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
	// The sandboxer binary is NOT copied into the image anymore — it is a host
	// tool, and egress is a separate squid sidecar (the flake's proxyImage).
	return nil
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
	// Inherit the host's proxy. The builder is a container, so it starts with
	// an EMPTY environment — without this it cannot reach the network on a
	// machine whose access goes through a proxy, and the failure is nasty
	// rather than obvious: the llm-agents binary cache is unreachable, nix
	// dutifully falls back (fallback true) to building every agent FROM
	// SOURCE, and each source build then curls a release tarball straight at
	// the internet until a five-minute timeout. Ten-plus minutes to fail at
	// fetching something the cache would have served. A localhost proxy is
	// rewritten to the host gateway (the user means "on my host"), which is
	// why the aliases go in too. Never logged — a proxy URL may carry
	// credentials.
	// …unless the user put the builder on the host's own network, where
	// localhost already IS the host: rewriting there would aim a working proxy
	// at the bridge gateway, which a loopback-bound proxy is not listening on.
	// That is the sane way to reach a SOCKS5 proxy on your machine.
	hostNet := hasHostNetwork(o.ExtraArgs)
	if proxyEnv := config.HostProxyEnv(!hostNet); len(proxyEnv) > 0 {
		for _, kv := range proxyEnv {
			args = append(args, "--env", kv)
		}
		if !hostNet {
			args = append(args, config.HostGatewayArgs()...)
		}
	}
	// Last, so an explicit --builder-arg overrides anything chosen above.
	args = append(args, o.ExtraArgs...)
	// A DestTar build (the microVM store) needs only the toolbox image, not the
	// egress squid proxyImage — building the proxy too would download its whole
	// closure for nothing.
	script := builderScript(o.Spec.NixpkgsRev, o.Spec.LLMAgentsRev)
	if o.DestTar != "" {
		script = builderScriptImage(o.Spec.NixpkgsRev, o.Spec.LLMAgentsRev)
	}
	args = append(args, o.NixImage, "sh", "-lc", script)
	return args
}

// hasHostNetwork reports whether the user's --builder-arg escape hatch put the
// builder on the host's network namespace, in either engine's spelling and in
// both the joined (--network=host) and split (--network host) forms. It only
// changes whether a localhost proxy URL is rewritten — see builderArgv.
func hasHostNetwork(extra []string) bool {
	for i, a := range extra {
		switch a {
		case "--network=host", "--net=host":
			return true
		case "--network", "--net":
			if i+1 < len(extra) && extra[i+1] == "host" {
				return true
			}
		}
	}
	return false
}

// builderScript is the in-container shell run by the nix builder: build the
// `#image` derivation from the mounted /src flake and copy the realized image
// tarball to the bind-mounted /out. `--accept-flake-config` is kept so any
// future flake nixConfig applies without a prompt — the embedded flake
// declares none today (llm-agents' binary cache was dropped, so agents compile
// from source). The substituter timeouts are load-bearing regardless:
// connect-timeout/stalled-download-timeout fail an unreachable or stalling
// substituter fast (nix's defaults hang for minutes on a geo-blocked mirror),
// and fallback lets nix carry on via cache.nixos.org or a source build instead
// of aborting. Note the LIMIT of that guard: both are substituter settings.
// The curl inside a fixed-output derivation — fetchurl pulling an agent's
// release tarball, which is exactly where a fallback-to-source build ends up —
// obeys nixpkgs' own 300s-with-retries instead, so an unreachable network
// still costs minutes there. `--no-write-lock-file` keeps the read-only /src untouched. A rev override
// becomes an --override-input flag only when it differs from the embedded pin
// — when the effective rev IS the pin the script stays byte-identical to a
// stock build, so nix's eval cache is shared. Revs are full 40-hex commits
// (ValidateImageSpec / ResolveLatest enforce it; BuildImage pins tracking revs
// before this renders), so the != comparison is exact and a github: flakeref
// override never needs a GitHub API resolve.
func builderScript(nixpkgsRev, llmAgentsRev string) string {
	// Build BOTH the toolbox image and the egress squid proxyImage in one nix
	// invocation (shared eval), copying each realized tarball to /out.
	nixBuild := nixBuildPrefix(overrideFlags(nixpkgsRev, llmAgentsRev))
	return "set -e; " +
		nixBuild + "path:/src#image > /out/storepath && " +
		`cp -L "$(cat /out/storepath)" /out/image.tar.gz && ` +
		nixBuild + "path:/src#proxyImage > /out/proxypath && " +
		`cp -L "$(cat /out/proxypath)" /out/proxy.tar.gz`
}

// builderScriptImage is builderScript's image-only variant, run by the microVM
// builder: the toolbox backend has no squid proxyImage (egress is smolvm's own
// --allow-host), so only path:/src#image is realized and copied to /out.
func builderScriptImage(nixpkgsRev, llmAgentsRev string) string {
	nixBuild := nixBuildPrefix(overrideFlags(nixpkgsRev, llmAgentsRev))
	return "set -e; " +
		nixBuild + "path:/src#image > /out/storepath && " +
		`cp -L "$(cat /out/storepath)" /out/image.tar.gz`
}

// overrideFlags renders the shared nix build flags: an --override-input for
// each rev that differs from the embedded pin (byte-identical to a stock build
// when the effective rev IS the pin, so nix's eval cache is shared). The revs
// are always concrete commits by the time this renders, so there is no mutable
// flakeref left for nix's own --refresh to matter to.
func overrideFlags(nixpkgsRev, llmAgentsRev string) string {
	flags := ""
	embNixpkgs, embLLMAgents := EmbeddedRevs()
	if nixpkgsRev != "" && nixpkgsRev != embNixpkgs {
		flags += "--override-input nixpkgs github:NixOS/nixpkgs/" + nixpkgsRev + " "
	}
	if llmAgentsRev != "" && llmAgentsRev != embLLMAgents {
		flags += "--override-input llm-agents github:numtide/llm-agents.nix/" + llmAgentsRev + " "
	}
	return flags
}

// nixBuildPrefix is the shared `nix … build …` command prefix (substituter
// timeouts + fallback so an unreachable or stalling substituter never wedges
// the build; see builderScript for the full rationale).
func nixBuildPrefix(flags string) string {
	return "nix --extra-experimental-features 'nix-command flakes' " +
		"--accept-flake-config " +
		"--option connect-timeout 5 --option stalled-download-timeout 30 --option fallback true " +
		"build " + flags + "--no-write-lock-file --no-link --print-out-paths "
}

// orEmpty keeps the rendered JSON an object ({} not null) for a nil map.
func orEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
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
