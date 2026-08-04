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
// the built tar lands at DestTar. The pins stamp is warm — there is no
// container engine here to resolve a cold one, and a cold cache is asserted to
// fail closed rather than silently building the embedded revs.
func TestBuildImageVM(t *testing.T) {
	warmPins(t)
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

	// A cold pins cache fails closed with image-build guidance (no engine to
	// resolve the tracking default against).
	pinsCacheDir(t)
	if err := BuildImageVM(BuildVMOpts{Smolvm: bin, DestTar: dest}); err == nil ||
		!strings.Contains(err.Error(), "image build") {
		t.Errorf("cold pins cache = %v, want a fail-closed error with image-build guidance", err)
	}
}

// TestVMBuilderArgv pins the microVM builder argv: nixos/nix run with the
// context and out dirs shared, the nix caches allow-listed, the image ceiling
// raised, and the image-only build script.
func TestVMBuilderArgv(t *testing.T) {
	clearProxyEnv(t) // no host proxy → the allow-host (direct-egress) branch
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
		"-e", "PATH=/root/.nix-profile/bin:/nix/var/nix/profiles/default/bin:/nix/var/nix/profiles/default/sbin",
		"-e", "USER=root",
		"-e", "NIX_SSL_CERT_FILE=/nix/var/nix/profiles/default/etc/ssl/certs/ca-bundle.crt",
		"-e", "SSL_CERT_FILE=/nix/var/nix/profiles/default/etc/ssl/certs/ca-bundle.crt",
		"-e", "GIT_SSL_CAINFO=/nix/var/nix/profiles/default/etc/ssl/certs/ca-bundle.crt",
		"-e", "NIX_PATH=/nix/var/nix/profiles/per-user/root/channels:/root/.nix-defexpr/channels",
		"--max-image-size", "16GiB",
		"--", "/bin/sh", "-lc", builderScriptImage("", ""),
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

// TestVMBuilderArgvProxy: with a host proxy, the builder opens the network and
// inherits the proxy env (so nix fetches through it) instead of allow-listing
// the caches for direct egress.
func TestVMBuilderArgvProxy(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv("http_proxy", "http://127.0.0.1:8888")
	t.Setenv("https_proxy", "http://127.0.0.1:8888")

	got := vmBuilderArgv(BuildVMOpts{NixImage: NixImage}, "/ctx", "/out")
	j := strings.Join(got, " ")
	if !slices.Contains(got, "--net") {
		t.Errorf("proxy build should open the network: %q", got)
	}
	if !strings.Contains(j, "http_proxy=http://127.0.0.1:8888") {
		t.Errorf("proxy env not inherited: %q", j)
	}
	if strings.Contains(j, "--allow-host") {
		t.Errorf("proxy build should not allow-list caches directly: %q", j)
	}
}

// TestBuilderScriptImage pins that the microVM build script realizes ONLY the
// toolbox image (no squid proxyImage, which the microVM backend does not use)
// and honors a rev override.
func TestBuilderScriptImage(t *testing.T) {
	s := builderScriptImage("", "")
	if !strings.Contains(s, "path:/src#image") {
		t.Errorf("script does not build the image: %q", s)
	}
	if strings.Contains(s, "proxyImage") {
		t.Errorf("microVM build must not build the proxy image: %q", s)
	}
	// A differing nixpkgs rev becomes an override-input flag.
	withRev := builderScriptImage("0000000000000000000000000000000000000000", "")
	if !strings.Contains(withRev, "--override-input nixpkgs") {
		t.Errorf("rev override missing: %q", withRev)
	}
}
