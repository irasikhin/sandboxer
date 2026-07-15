package toolbox

import (
	"strings"
	"testing"
)

// TestFlakeBakesTmux guards that the embedded toolbox flake keeps the
// persistent-session pieces: the tmux server (plus the ncurses terminfo it
// needs) in the image contents, and the baked /etc/sandboxer/tmux.conf whose
// default-command routes every window through the rc.sh launcher, with the
// Ctrl-q detach binding and a deep scrollback.
func TestFlakeBakesTmux(t *testing.T) {
	data, err := assets.ReadFile("assets/flake.nix")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{
		`writeTextDir "etc/sandboxer/tmux.conf"`,
		"detach-client",
		"history-limit",
		"default-command",
		"tmux",
		"ncurses",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("embedded flake.nix missing %q — tmux session support not wired", want)
		}
	}
}
