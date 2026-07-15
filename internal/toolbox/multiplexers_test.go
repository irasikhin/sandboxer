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
	data, err := assets.ReadFile("assets/flake.nix")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{
		`writeTextDir "etc/tmux.conf"`,
		"history-limit",
		"default-command",
		"tmux",
		"zellij",
		"ncurses",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("embedded flake.nix missing %q — opt-in multiplexers not wired", want)
		}
	}
}
