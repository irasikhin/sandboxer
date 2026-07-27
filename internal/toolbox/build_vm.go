package toolbox

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/irasikhin/sandboxer/internal/config"
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

// nixImageEnv is the subset of the pinned nixos/nix image's Config.Env the
// in-VM build needs, set EXPLICITLY because smolvm — unlike docker/podman — does
// not apply an OCI image's environment. Without PATH the build fails before it
// starts ("executable file `sh` not found"); without the SSL cert vars nix
// cannot fetch from the binary caches over TLS. The paths are profile-relative
// and stable across nixos/nix versions.
var nixImageEnv = []string{
	"PATH=/root/.nix-profile/bin:/nix/var/nix/profiles/default/bin:/nix/var/nix/profiles/default/sbin",
	"USER=root",
	"NIX_SSL_CERT_FILE=/nix/var/nix/profiles/default/etc/ssl/certs/ca-bundle.crt",
	"SSL_CERT_FILE=/nix/var/nix/profiles/default/etc/ssl/certs/ca-bundle.crt",
	"GIT_SSL_CAINFO=/nix/var/nix/profiles/default/etc/ssl/certs/ca-bundle.crt",
	"NIX_PATH=/nix/var/nix/profiles/per-user/root/channels:/root/.nix-defexpr/channels",
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
	// The build must reach the nix caches. If the host uses a proxy (the homelab
	// case where direct egress is blocked/reset), inherit it and open the network
	// so the in-VM nix fetches through it — smolvm's TSI reaches a host-local
	// proxy, so localhost is NOT rewritten. Without a host proxy, allow-list the
	// cache hosts for direct egress (the fail-closed default). Mirrors the
	// container builder's HostProxyEnv inheritance.
	if proxyEnv := config.HostProxyEnv(false); len(proxyEnv) > 0 {
		args = append(args, "--net")
		for _, kv := range proxyEnv {
			args = append(args, "-e", kv)
		}
	} else {
		for _, h := range vmImageAllowHosts {
			args = append(args, "--allow-host", h)
		}
	}
	// smolvm does not apply the image's Config.Env, so the nixos/nix environment
	// is set explicitly here (see nixImageEnv).
	for _, e := range nixImageEnv {
		args = append(args, "-e", e)
	}
	// The toolbox tar is larger than smolvm's 8 GiB default acceptance ceiling.
	args = append(args, "--max-image-size", "16GiB")
	// /bin/sh is an absolute symlink to bash in the image, so it is exec'able
	// regardless of PATH; nixImageEnv's PATH then lets the script reach `nix`.
	args = append(args, "--", "/bin/sh", "-lc",
		builderScriptImage(o.Refresh, o.Spec.NixpkgsRev, o.Spec.LLMAgentsRev))
	return args
}
