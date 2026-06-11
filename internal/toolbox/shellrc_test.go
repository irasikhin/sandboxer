package toolbox

import (
	"strings"
	"testing"
)

// TestFlakeEmbedsShellRc guards that the embedded toolbox flake still bakes the
// interactive-shell rc into the image (the sandbox-aware prompt and the
// plugin/user drop-in hooks), so a refactor of the asset cannot silently drop
// the terminal UX or the `enter` launcher's `/etc/sandboxer/rc.sh` target.
func TestFlakeEmbedsShellRc(t *testing.T) {
	data, err := assets.ReadFile("assets/flake.nix")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{
		`writeTextDir "etc/sandboxer/rc.sh"`,
		"shellRc",
		"SANDBOXER_SLUG",
		"/etc/sandboxer/rc.d",
		".config/sandboxer/rc",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("embedded flake.nix missing %q — shell rc not wired", want)
		}
	}
}

// TestFlakeBakesToolingPack guards that the baseline tooling humans and agents
// rely on (pager, editor, process tools, search, archives, delta git pager)
// stays baked into the image, and that /etc/gitconfig routes the pager through
// delta.
func TestFlakeBakesToolingPack(t *testing.T) {
	data, err := assets.ReadFile("assets/flake.nix")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{
		"less", "neovim", "procps", "ripgrep", "fd", "tree",
		"gnutar", "gzip", "delta", "gnumake", "unzip",
		`writeTextDir "etc/gitconfig"`, "gitConfig",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("embedded flake.nix missing tooling %q", want)
		}
	}
}
