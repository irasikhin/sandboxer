package toolbox

import (
	"strings"
	"testing"
)

// TestFlakeBakesTmux pins the multiplexer layer: tmux ships in the image
// (plus the terminfo it needs) with a system /etc/tmux.conf whose
// default-command routes every window through the rc.sh launcher — enter
// attaches it (tmuxEnterArgs). zellij is gone: one multiplexer, one set of
// bindings, one thing to document.
func TestFlakeBakesTmux(t *testing.T) {
	s := imageDefinition(t)
	for _, want := range []string{
		`writeTextDir "etc/tmux.conf"`,
		"history-limit",
		"default-command",
		"tmux",
		"ncurses",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("images.nix missing %q — the tmux layer is not wired", want)
		}
	}
	if strings.Contains(s, "zellij") {
		t.Error("images.nix still ships zellij — removed in favor of tmux only")
	}
}
