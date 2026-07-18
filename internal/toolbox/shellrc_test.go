package toolbox

import (
	"strings"
	"testing"
)

// imageDefinition returns assets/images.nix — the shared definition of what is
// IN the images, imported by BOTH the embedded flake and the repo's root flake.
// The content guards below read it rather than flake.nix, which now only
// resolves a profile's context.
func imageDefinition(t *testing.T) string {
	t.Helper()
	data, err := assets.ReadFile("assets/images.nix")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestFlakeImportsImages guards the seam itself: the embedded flake must import
// the shared image definition, and it must be rendered into the build context
// (see writeContext). Without this the content guards below could all pass
// while the flake builds nothing.
func TestFlakeImportsImages(t *testing.T) {
	data, err := assets.ReadFile("assets/flake.nix")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "import ./images.nix") {
		t.Error("embedded flake.nix must import ./images.nix — the shared image definition is not wired")
	}
}

// TestFlakeEmbedsShellRc guards that the toolbox image still bakes the
// interactive-shell rc (the sandbox-aware prompt and the plugin/user drop-in
// hooks), so a refactor cannot silently drop the terminal UX or the `enter`
// launcher's `/etc/sandboxer/rc.sh` target.
func TestFlakeEmbedsShellRc(t *testing.T) {
	s := imageDefinition(t)
	for _, want := range []string{
		`writeTextDir "etc/sandboxer/rc.sh"`,
		"shellRc",
		"SANDBOXER_SLUG",
		"/etc/sandboxer/rc.d",
		".config/sandboxer/rc",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("images.nix missing %q — shell rc not wired", want)
		}
	}
}

// TestFlakeBakesToolingPack guards that the baseline tooling humans and agents
// rely on (pager, editor, process tools, search, archives, delta git pager)
// stays baked into the image, and that /etc/gitconfig routes the pager through
// delta.
func TestFlakeBakesToolingPack(t *testing.T) {
	s := imageDefinition(t)
	for _, want := range []string{
		"less", "neovim", "procps", "ripgrep", "fd", "tree",
		"gnutar", "gzip", "delta", "gnumake", "unzip",
		`writeTextDir "etc/gitconfig"`, "gitConfig",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("images.nix missing tooling %q", want)
		}
	}
}

// TestImageBakesNestedPodman guards the nested-podman layer end to end: the
// runtime pieces, and the two image-side bits without which a rootless podman
// inside the sandbox cannot pull anything — /var/tmp (containers/image stages
// blobs there) and a storage.conf whose ignore_chown_errors absorbs the
// single-uid mapping. The launcher half is backend.nestedContainerArgs.
func TestImageBakesNestedPodman(t *testing.T) {
	s := imageDefinition(t)
	for _, want := range []string{
		"podman", "crun", "conmon", "netavark", "aardvark-dns", "passt", "fuse-overlayfs",
		`writeTextDir "etc/containers/policy.json"`,
		`writeTextDir "etc/containers/storage.conf"`,
		"ignore_chown_errors",
		"/var/tmp",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("images.nix missing nested-podman piece %q", want)
		}
	}
}

// TestImageBakesPythonBatteries guards that the base python3 carries the glue
// libraries baked into the image (click CLIs, YAML config, jinja2 templating),
// via python3.withPackages — a plain python3 would import-error on them.
func TestImageBakesPythonBatteries(t *testing.T) {
	s := imageDefinition(t)
	if !strings.Contains(s, "python3.withPackages") {
		t.Error("images.nix ships a bare python3 — the batteries (click/pyyaml/jinja2) are not wired")
	}
	for _, want := range []string{"click", "pyyaml", "jinja2"} {
		if !strings.Contains(s, want) {
			t.Errorf("images.nix python batteries missing %q", want)
		}
	}
}
