package toolbox

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// The host-nix image build: the microVM backends realize the toolbox image by
// running nix on the HOST — the same `path:<ctx>#image` derivation the container
// builder ran in an ephemeral nixos/nix container and the in-VM builders ran in
// a microVM guest. Host nix is chosen because it is already a hard requirement
// of the CLI (sandboxer.nix is evaluated with host nix-instantiate on every
// invocation), because it reuses the user's own nix store and binary caches, and
// because it deletes the whole builder-guest machinery (proxy-env inheritance,
// per-host egress rules, an image's Config.Env, machine sizing, a cache share).
// Like the container DestTar build there is no squid proxyImage (microVM egress
// is the runner's own policy engine), so only path:/src#image is realized.

// BuildHostNixOpts configures a host-nix toolbox image build.
type BuildHostNixOpts struct {
	Spec    Spec      // image variant customization; zero = stock
	DestTar string    // where the built image tar is placed (required)
	Stderr  io.Writer // progress banners (nix's own chatter goes to stderr)
}

// hostNixArgv is the nix argv that realizes path:<ctx>#image and prints the
// resulting store path. The --extra-experimental-features is belt-and-braces for
// a host whose nix has not opted into flakes; --accept-flake-config keeps the
// flake's (currently empty) nixConfig from prompting; --no-link avoids leaving a
// ./result beside the context; --print-out-paths is how the caller learns where
// the tar landed. Pure, so it is golden-tested without nix.
//
// The spec's resolved revs ride as --override-input: writeContext copies the
// embedded flake VERBATIM, so its committed input pins are what nix would
// otherwise build — every image build would silently reproduce the embedded
// snapshot no matter what PinSpec stamped, and "latest" agents would never
// arrive (that was live: guests stayed on a May claude-code while the pins
// cache said August). A rev that is still tracking ("", "latest") is skipped —
// nothing concrete to override with — which keeps the argv valid for callers
// that never pinned.
func hostNixArgv(ctxDir string, spec Spec) []string {
	args := []string{
		"--extra-experimental-features", "nix-command flakes",
		"--accept-flake-config",
		"build", "--no-link", "--print-out-paths",
	}
	if !isLatestRev(spec.NixpkgsRev) {
		args = append(args, "--override-input", "nixpkgs", "github:NixOS/nixpkgs/"+spec.NixpkgsRev)
	}
	return append(args, "path:"+ctxDir+"#image")
}

// BuildImageHostNix assembles the build context and realizes path:<ctx>#image on
// the host, copying the built tar to DestTar. It requires nix on the host —
// already a hard requirement of the CLI — and nothing else.
func BuildImageHostNix(o BuildHostNixOpts) error {
	if o.DestTar == "" {
		return errors.New("no destination tar for the host-nix image build")
	}
	if _, err := exec.LookPath("nix"); err != nil {
		return errors.New("the toolbox image is built with host nix, and nix is not installed — install nix and retry")
	}
	progress := o.Stderr
	if progress == nil {
		progress = io.Discard
	}

	// Resolve tracking input revs from the pins cache, resolving on the host via
	// git when they are cold — a container-less host resolves exactly like one
	// with docker/podman, so a first-ever build never needs an engine.
	spec, err := PinSpec(o.Spec, false, progress)
	if err != nil {
		return fmt.Errorf("host-nix image build: %w", err)
	}
	o.Spec = spec

	ctxDir, err := os.MkdirTemp("", "sandboxer-nixctx-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(ctxDir) }()

	if err := writeContext(ctxDir, o.Spec); err != nil {
		return fmt.Errorf("assemble build context: %w", err)
	}

	fmt.Fprintf(progress, "sandboxer: building toolbox image with host nix "+
		"(several minutes on first run)…\n")

	build := exec.Command("nix", hostNixArgv(ctxDir, o.Spec)...)
	// nix's progress goes to stderr; stdout carries only the printed store path.
	build.Stderr = o.Stderr
	out, err := build.Output()
	if err != nil {
		return fmt.Errorf("toolbox image build (host nix) failed: %w", err)
	}
	// `--print-out-paths` prints one output path per line; the image derivation
	// has a single output, but take the LAST non-empty line so a future
	// multi-output derivation resolves to its final path instead of failing on a
	// path that includes a newline.
	storePath := ""
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			storePath = line
		}
	}
	if storePath == "" {
		return errors.New("toolbox image build (host nix) produced no store path")
	}
	if err := copyFile(storePath, o.DestTar); err != nil {
		return fmt.Errorf("store built image: %w", err)
	}
	return nil
}
