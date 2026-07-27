package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/toolbox"
)

// The microVM image "store". A container engine keeps images in its own
// content-addressed store and `machine create -I name:tag` would make smolvm
// pull a registry ref — but the toolbox image is built locally and never
// published, so for the microVM backend an image is a docker-save tar on disk:
// one file per name under <state root>/images/<name>.tar, with its sha256 in a
// sibling .sha256 sidecar (computed once at store time — never rehash a
// multi-GB tar on every enter). vmEnsureImage resolves an image NAME to the tar
// PATH smolvm is actually handed, building it in a microVM on first use exactly
// as the container path auto-builds on first enter.

// vmImagesDir is the per-host image store directory, or "" when no state root
// exists.
func vmImagesDir() string {
	root := config.StateRoot()
	if root == "" {
		return ""
	}
	return filepath.Join(root, "images")
}

// vmImagePath is the tar path an image name maps to (the name sanitized to the
// same filesystem-safe alphabet a container name uses).
func vmImagePath(image string) string {
	dir := vmImagesDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, sanitizeContainerName(image)+".tar")
}

// vmImageExists reports whether the image's tar is present in the store.
func vmImageExists(image string) bool {
	p := vmImagePath(image)
	return p != "" && pathExists(p)
}

// vmImageID returns the stored image's content id (sha256 of the tar, hex). It
// reads the .sha256 sidecar written at store time; if the sidecar is missing but
// the tar is present it computes the digest once and caches it. "" when the
// image is absent or unreadable, which skips the freshness check (never a false
// "stale").
func vmImageID(image string) string {
	p := vmImagePath(image)
	if p == "" || !pathExists(p) {
		return ""
	}
	if data, err := os.ReadFile(p + ".sha256"); err == nil {
		if id := string(data); id != "" {
			return id
		}
	}
	id, err := fileSHA256(p)
	if err != nil {
		return ""
	}
	_ = os.WriteFile(p+".sha256", []byte(id), 0o600)
	return id
}

// vmRemoveImage deletes an image's tar and its sidecar (idempotent).
func vmRemoveImage(image string) error {
	p := vmImagePath(image)
	if p == "" {
		return nil
	}
	_ = os.Remove(p + ".sha256")
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// vmStoreImage moves a freshly built tar into the store under image's name
// (atomic rename within the store dir) and records its sha256 sidecar.
func vmStoreImage(image, srcTar string) error {
	dst := vmImagePath(image)
	if dst == "" {
		return errNoStateRoot
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	id, err := fileSHA256(srcTar)
	if err != nil {
		return err
	}
	if err := os.Rename(srcTar, dst); err != nil {
		return err
	}
	return os.WriteFile(dst+".sha256", []byte(id), 0o600)
}

// vmEnsureImage resolves o.Image to the value smolvm's -I is handed: a public
// ref passes straight through (smolvm pulls it), while the locally-built toolbox
// image (the default, or any variant with a non-empty spec — the same condition
// the container ensureImage builds under) resolves to its store tar, built in a
// microVM on first use unless SANDBOXER_NO_AUTOBUILD is set.
func vmEnsureImage(o RunOpts) (string, error) {
	if o.Image != config.DefaultImage && o.Spec.Empty() {
		return o.Image, nil // a custom public image — let smolvm pull it
	}
	if vmImageExists(o.Image) {
		return vmImagePath(o.Image), nil
	}
	hint := "sandboxer image build --backend microvm"
	if !o.Spec.Empty() {
		hint = "sandboxer image build --backend microvm <profile> (this variant image needs its profile)"
	}
	if os.Getenv("SANDBOXER_NO_AUTOBUILD") != "" {
		return "", fmt.Errorf("toolbox image %q is not in the microvm image store "+
			"and is built locally (never published) — build it with:\n  %s", o.Image, hint)
	}
	if o.Stderr != nil {
		fmt.Fprintf(o.Stderr, "sandboxer: toolbox image %q not found — building it now "+
			"(one-time, several minutes; disable with SANDBOXER_NO_AUTOBUILD=1)…\n", o.Image)
	}
	if err := vmBuildImageToStore(o); err != nil {
		return "", fmt.Errorf("%w — build manually with: %s", err, hint)
	}
	if !vmImageExists(o.Image) {
		return "", fmt.Errorf("toolbox image %q still missing after build — try: %s", o.Image, hint)
	}
	return vmImagePath(o.Image), nil
}

// BuildVMImage builds the toolbox image in a microVM and stores it under the
// image name — the explicit `sandboxer image build --backend microvm` entry
// point, the counterpart of the enter-time auto-build (vmEnsureImage). It
// rebuilds unconditionally (an explicit build always refreshes the store) and
// errors up front if smolvm is unavailable, so the CLI never silently no-ops.
func BuildVMImage(image string, spec toolbox.Spec, stderr io.Writer) error {
	if _, err := resolveSmolvm(); err != nil {
		return err
	}
	return vmBuildImageToStore(RunOpts{Image: image, Spec: spec, Stderr: stderr})
}

// vmBuildImageToStore builds the toolbox image inside a microVM (no container
// engine anywhere) and stores the resulting tar. A package var so a test can
// stand in for the minutes-long, network-bound real build.
var vmBuildImageToStore = func(o RunOpts) error {
	tmp, err := os.MkdirTemp("", "sandboxer-vm-img-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	out := filepath.Join(tmp, "image.tar")

	// Prefer a container engine for the one-time image build when one is present:
	// it is reliable and the resulting tar boots under smolvm. The in-VM builder
	// (no engine) is the fallback — but a smolvm registry-pull bug currently
	// leaves a nixos/nix guest without its /nix/store, so it is best-effort until
	// that is fixed upstream or the builder image is loaded rather than pulled.
	if engine, derr := DetectEngine(config.LoadDefaults()); derr == nil {
		if o.Stderr != nil {
			fmt.Fprintf(o.Stderr, "sandboxer: building the toolbox image with %s, then storing it for the microvm backend…\n", engine)
		}
		bo := toolbox.BuildOpts{
			Engine: engine, Image: o.Image, Spec: o.Spec, DestTar: out,
			// A persistent nix-store cache makes the download resumable — key on a
			// flaky link where individual NAR fetches stall and retry.
			Cache:  true,
			Stdout: o.Stderr, Stderr: o.Stderr,
		}
		// A loopback-bound host proxy (the common tunnel-client case) is
		// unreachable from the builder's bridge network — the default
		// host.docker.internal rewrite points at the gateway, not the
		// loopback port, and the build stalls fetching flake inputs. Give the
		// builder the host network so localhost really is the host's loopback.
		if hostProxyIsLoopback() {
			bo.ExtraArgs = []string{"--network=host"}
		}
		if err := toolbox.BuildImage(bo); err != nil {
			return err
		}
		return vmStoreImage(o.Image, out)
	}
	if err := toolbox.BuildImageVM(toolbox.BuildVMOpts{
		Smolvm:  smolvmBin(),
		DestTar: out,
		Spec:    o.Spec,
		Stdout:  o.Stderr,
		Stderr:  o.Stderr,
	}); err != nil {
		return err
	}
	return vmStoreImage(o.Image, out)
}

// hostProxyIsLoopback reports whether the host's proxy env points at a loopback
// address — a proxy a bridge-networked builder cannot reach (its
// host.docker.internal rewrite hits the gateway, not the loopback-bound port),
// so the build must run on the host network instead.
func hostProxyIsLoopback() bool {
	for _, name := range []string{"https_proxy", "HTTPS_PROXY", "http_proxy", "HTTP_PROXY", "all_proxy", "ALL_PROXY"} {
		v := os.Getenv(name)
		if v == "" {
			continue
		}
		u, err := url.Parse(v)
		if err != nil {
			continue
		}
		h := u.Hostname()
		if h == "localhost" || h == "::1" || strings.HasPrefix(h, "127.") {
			return true
		}
	}
	return false
}

// fileSHA256 returns the hex sha256 of a file's contents, streamed so a
// multi-GB image tar is never held in memory.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
