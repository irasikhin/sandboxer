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
