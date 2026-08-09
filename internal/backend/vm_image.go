package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

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

// BuildVMImage builds the toolbox image for a microVM backend and stores it
// under the image name — the explicit `sandboxer image build --backend
// microvm|microsandbox` entry point, the counterpart of the enter-time
// auto-build. It rebuilds unconditionally (an explicit build always refreshes
// the store) and errors up front if the requested runner is unavailable, so the
// CLI never silently no-ops. Both runners share ONE build artifact (the tar);
// microsandbox additionally imports it into its own image store, which is what
// its `create` reads.
func BuildVMImage(engine, image string, spec toolbox.Spec, stderr io.Writer) error {
	if engine == msbEngine {
		if _, err := resolveMsb(); err != nil {
			return err
		}
	} else if _, err := resolveSmolvm(); err != nil {
		return err
	}
	if err := vmBuildImageToStore(RunOpts{Engine: engine, Image: image, Spec: spec, Stderr: stderr}); err != nil {
		return err
	}
	if engine == msbEngine {
		return msbLoadStoredImage(image, stderr)
	}
	return nil
}

// vmBuildImageToStore builds the toolbox image and stores it under o.Image's
// name. The image is built with HOST NIX — nix is already a hard requirement of
// the CLI (sandboxer.nix eval), so there is no builder container and no builder
// guest, and both microVM runners share the one tar. A package var so a test can
// stand in for the minutes-long, network-bound real build.
var vmBuildImageToStore = func(o RunOpts) error {
	dir := vmImagesDir()
	if dir == "" {
		return errNoStateRoot
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// The temp dir lives INSIDE the store so vmStoreImage's rename never
	// crosses a filesystem boundary (/tmp is often tmpfs — EXDEV).
	tmp, err := os.MkdirTemp(dir, ".build-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	out := filepath.Join(tmp, "image.tar")

	if o.Stderr != nil {
		fmt.Fprintf(o.Stderr, "sandboxer: building the toolbox image with host nix, "+
			"then storing it for the microVM backend…\n")
	}
	if err := toolbox.BuildImageHostNix(toolbox.BuildHostNixOpts{
		Spec: o.Spec, DestTar: out, Stderr: o.Stderr,
	}); err != nil {
		return err
	}
	return vmStoreImage(o.Image, out)
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
