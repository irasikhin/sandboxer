package toolbox

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// buildFakeSmolvm is a smolvm stand-in for the image build: it finds the
// `<hostOutDir>:/out` volume in its argv and drops a stand-in image tar there,
// so BuildImageVM's orchestration (context assembly → run → store) runs without
// a hypervisor or a real nix build.
const buildFakeSmolvm = `#!/usr/bin/env bash
set -euo pipefail
out=""
for a in "$@"; do
  case "$a" in *:/out) out="${a%%:/out}";; esac
done
[ -n "$out" ] && printf 'IMAGE-TAR' > "$out/image.tar.gz"
exit 0
`

// TestBuildImageVM pins the in-VM build orchestration end to end (fake smolvm):
// the built tar lands at DestTar.
func TestBuildImageVM(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "smolvm")
	if err := os.WriteFile(bin, []byte(buildFakeSmolvm), 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "toolbox.tar")
	if err := BuildImageVM(BuildVMOpts{Smolvm: bin, DestTar: dest}); err != nil {
		t.Fatalf("BuildImageVM: %v", err)
	}
	data, err := os.ReadFile(dest)
	if err != nil || string(data) != "IMAGE-TAR" {
		t.Errorf("built tar = %q, %v; want IMAGE-TAR at DestTar", data, err)
	}

	// Missing required options fail loudly.
	if err := BuildImageVM(BuildVMOpts{DestTar: dest}); err == nil {
		t.Error("BuildImageVM with no smolvm must error")
	}
	if err := BuildImageVM(BuildVMOpts{Smolvm: bin}); err == nil {
		t.Error("BuildImageVM with no DestTar must error")
	}
}

// TestVMBuilderArgv pins the microVM builder argv: nixos/nix run with the
// context and out dirs shared, the nix caches allow-listed, the image ceiling
// raised, and the image-only build script.
func TestVMBuilderArgv(t *testing.T) {
	o := BuildVMOpts{Smolvm: "smolvm", NixImage: NixImage}
	got := vmBuilderArgv(o, "/ctx", "/out")
	want := []string{
		"machine", "run", "-I", NixImage,
		"-v", "/ctx:/src:ro",
		"-v", "/out:/out",
		"--allow-host", "cache.nixos.org",
		"--allow-host", "channels.nixos.org",
		"--allow-host", "github.com",
		"--allow-host", "objects.githubusercontent.com",
		"--allow-host", "raw.githubusercontent.com",
		"--allow-host", "numtide.cachix.org",
		"--max-image-size", "16GiB",
		"--", "sh", "-lc", builderScriptImage(false, "", ""),
	}
	if !slices.Equal(got, want) {
		t.Errorf("vmBuilderArgv =\n%q\nwant\n%q", got, want)
	}

	// A cache dir adds one more volume, before the allow-hosts.
	withCache := vmBuilderArgv(BuildVMOpts{NixImage: NixImage, Cache: "/nixcache"}, "/ctx", "/out")
	if !slices.Contains(withCache, "/nixcache:/nix") {
		t.Errorf("cache volume missing: %q", withCache)
	}
}

// TestBuilderScriptImage pins that the microVM build script realizes ONLY the
// toolbox image (no squid proxyImage, which the microVM backend does not use)
// and honors a rev override.
func TestBuilderScriptImage(t *testing.T) {
	s := builderScriptImage(false, "", "")
	if !strings.Contains(s, "path:/src#image") {
		t.Errorf("script does not build the image: %q", s)
	}
	if strings.Contains(s, "proxyImage") {
		t.Errorf("microVM build must not build the proxy image: %q", s)
	}
	// A differing nixpkgs rev becomes an override-input flag.
	withRev := builderScriptImage(false, "0000000000000000000000000000000000000000", "")
	if !strings.Contains(withRev, "--override-input nixpkgs") {
		t.Errorf("rev override missing: %q", withRev)
	}
}
