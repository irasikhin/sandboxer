package toolbox

import (
	"strings"
	"testing"
)

// TestFlakeBakesMultiplexers pins the OPT-IN multiplexer layer: tmux and
// zellij ship in the image (plus the terminfo they need) with a system
// /etc/tmux.conf whose default-command routes every window through the rc.sh
// launcher — but sandboxer itself never starts or attaches them.
func TestFlakeBakesMultiplexers(t *testing.T) {
	s := imageDefinition(t)
	for _, want := range []string{
		`writeTextDir "etc/tmux.conf"`,
		"history-limit",
		"default-command",
		"tmux",
		"zellij",
		"ncurses",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("images.nix missing %q — opt-in multiplexers not wired", want)
		}
	}
}
