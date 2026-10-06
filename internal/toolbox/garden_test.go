package toolbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFlakeBakesGardenCli guards the Garden CLI layer. garden.io is not in
// nixpkgs, so the release binary is vendored (assets/garden/package.nix) — and
// it is a SELF-EXTRACTING launcher: on first run it unpacks its own Node
// runtime under the user's data dir and spawns it. That node is a foreign
// dynamically-linked ELF, so the image owes it two things the nix store does
// not provide at the standard paths: an interpreter at /lib64/ld-linux-… (or
// the aarch64 spelling) and libstdc++ on LD_LIBRARY_PATH. Both are wired here;
// dropping either makes every `garden` invocation die at spawn with "Could not
// start dynamically linked executable".
func TestFlakeBakesGardenCli(t *testing.T) {
	s := imageDefinition(t)
	for _, want := range []string{
		// the wrapped CLI, not the bare unpatched package: the wrapper is what
		// puts gcc's lib dir on the self-extracted node's LD_LIBRARY_PATH.
		"gardenCli",
		`${pkgs.garden}/bin/garden`,
		"--prefix LD_LIBRARY_PATH : ${pkgs.stdenv.cc.cc.lib}/lib",
		// the standard loader path for foreign binaries, both architectures
		"loaderCompat",
		"ld-linux-x86-64.so.2",
		"ld-linux-aarch64.so.1",
		"${pkgs.glibc}",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("images.nix missing garden piece %q", want)
		}
	}
	if strings.Contains(s, "--set LD_LIBRARY_PATH") {
		t.Error("garden's wrapper uses --set — it would clobber a caller's LD_LIBRARY_PATH (use --prefix)")
	}
}

// TestEmbeddedFlakeGraftsGarden guards that the vendored garden expression is
// grafted by the overlay of BOTH flakes: the embedded one (the image build
// `sandboxer image build` runs, whose context is rendered from the embedded
// assets) and the repo's root flake. One file, two callers — a dropped graft
// would leave pkgs.garden undefined in that caller's build.
func TestEmbeddedFlakeGraftsGarden(t *testing.T) {
	data, err := assets.ReadFile("assets/flake.nix")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "garden = final.callPackage ./garden/package.nix { };") {
		t.Error("embedded flake does not graft garden — the image build would fail on an undefined pkgs.garden")
	}
	// The repo's root flake is not embedded; tests run in the package dir.
	root, err := os.ReadFile(filepath.Join("..", "..", "flake.nix"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(root), "internal/toolbox/assets/garden/package.nix") {
		t.Error("root flake does not graft the vendored garden package (single source of truth)")
	}
}
