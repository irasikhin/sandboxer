package toolbox

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// The microVM image build: the container backend realizes the toolbox image by
// driving an ephemeral `nixos/nix` CONTAINER via docker/podman (build.go); the
// microVM backend realizes the same image by driving `nixos/nix` as a MICROVM
// via smolvm — no container engine anywhere. The build context and the flake
// are identical, so the image a microVM user gets cannot drift from the
// container one; only the runner and the "load" differ. Unlike the container
// build there is no squid proxyImage to build (the microVM backend's egress is
// smolvm's own --allow-host), so only path:/src#image is realized.

// vmImageAllowHosts is the egress allowlist the in-VM nix build needs: the
// binary caches and the flake input hosts. A smolvm machine has no route
// without these, so the build would otherwise fail closed.
var vmImageAllowHosts = []string{
	"cache.nixos.org",
	"channels.nixos.org",
	"github.com",
	"objects.githubusercontent.com",
	"raw.githubusercontent.com",
	"numtide.cachix.org",
}

// BuildVMOpts configures an in-microVM toolbox image build.
type BuildVMOpts struct {
	Smolvm   string    // resolved smolvm binary (required)
	DestTar  string    // where the built image tar is placed (required)
	NixImage string    // builder image override; "" → NixImage
	Cache    string    // host dir persisted as the guest /nix store; "" → none
	Spec     Spec      // image variant customization; zero = stock
	Refresh  bool      // re-fetch flake inputs
	Stdout   io.Writer // build chatter
	Stderr   io.Writer // progress banners
}

// BuildImageVM assembles the build context, realizes path:/src#image inside a
// `nixos/nix` microVM, and moves the resulting tar to DestTar. It requires no
// container engine — only smolvm and KVM/HVF.
func BuildImageVM(o BuildVMOpts) error {
	if o.Smolvm == "" {
		return fmt.Errorf("no smolvm binary for the microvm image build")
	}
	if o.DestTar == "" {
		return fmt.Errorf("no destination tar for the microvm image build")
	}
	if o.NixImage == "" {
		o.NixImage = NixImage
	}
	progress := o.Stderr
	if progress == nil {
		progress = io.Discard
	}

	ctxDir, err := os.MkdirTemp("", "sandboxer-vmctx-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(ctxDir) }()
	outDir, err := os.MkdirTemp("", "sandboxer-vmout-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(outDir) }()

	if err := writeContext(ctxDir, o.Spec); err != nil {
		return fmt.Errorf("assemble build context: %w", err)
	}

	fmt.Fprintf(progress, "sandboxer: building toolbox image via a microVM + %s "+
		"(several minutes on first run)…\n", o.NixImage)

	build := exec.Command(o.Smolvm, vmBuilderArgv(o, ctxDir, outDir)...)
	build.Stdout = o.Stdout
	build.Stderr = o.Stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("toolbox image build (microvm) failed: %w", err)
	}

	built := filepath.Join(outDir, "image.tar.gz")
	if err := os.Rename(built, o.DestTar); err != nil {
		// A cross-filesystem rename (temp dir vs state dir on another mount)
		// falls back to a copy.
		if err := copyFile(built, o.DestTar); err != nil {
			return fmt.Errorf("store built image: %w", err)
		}
	}
	return nil
}

// vmBuilderArgv assembles the smolvm argv that realizes the toolbox image in a
// microVM: the build context shared read-write (the guest writes the tar to
// /out), an optional persistent /nix cache dir, the nix binary-cache + flake
// hosts allow-listed, the image size ceiling raised for the multi-GB toolbox
// tar, and the same nix-build script the container builder runs (image only).
func vmBuilderArgv(o BuildVMOpts, ctxDir, outDir string) []string {
	args := []string{
		"machine", "run", "-I", o.NixImage,
		"-v", ctxDir + ":/src:ro",
		"-v", outDir + ":/out",
	}
	if o.Cache != "" {
		args = append(args, "-v", o.Cache+":/nix")
	}
	for _, h := range vmImageAllowHosts {
		args = append(args, "--allow-host", h)
	}
	// The toolbox tar is larger than smolvm's 8 GiB default acceptance ceiling.
	args = append(args, "--max-image-size", "16GiB")
	args = append(args, "--", "sh", "-lc",
		builderScriptImage(o.Refresh, o.Spec.NixpkgsRev, o.Spec.LLMAgentsRev))
	return args
}
