package toolbox

import (
	"strings"
	"testing"
	"unicode"
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

// TestTmuxConfUsability pins the settings that make tmux usable for the agent
// TUIs the sandbox exists to run — each one is a bug we would otherwise hit
// again: a 500ms escape-time makes ESC feel broken in vim/agent TUIs, RGB
// terminal-features keeps the 24-bit status palette from being quantized to
// muddy 256-colour approximations, and OSC 52 (set-clipboard) is the only way
// a yank inside the container reaches the host clipboard.
func TestTmuxConfUsability(t *testing.T) {
	s := imageDefinition(t)
	for want, why := range map[string]string{
		"escape-time 10":             "ESC would lag by half a second in every TUI",
		`terminal-features ",*:RGB"`: "the 24-bit status palette would be quantized",
		"set-clipboard on":           "a yank inside the sandbox would not reach the host clipboard",
		"allow-passthrough on":       "image/hyperlink escape sequences would be swallowed",
		"prefix C-Space":             "the documented prefix would not match the image",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("images.nix missing %q — %s", want, why)
		}
	}
}

// TestTmuxConfHasNoPrivateUseGlyphs guards the deliberate choice behind the
// status bar's looks: powerline separators (U+E0B0 and friends) live in the
// Unicode Private Use Area and render only with a patched Nerd Font. The HOST
// terminal's font is not ours to choose, so a PUA glyph anywhere in the image
// definition means somebody's status bar is a row of tofu boxes.
func TestTmuxConfHasNoPrivateUseGlyphs(t *testing.T) {
	for i, r := range imageDefinition(t) {
		if unicode.In(r, unicode.Co) {
			t.Errorf("images.nix byte %d: private-use glyph %q (U+%04X) — it needs a "+
				"patched Nerd Font on the host; use Block Elements or ASCII", i, r, r)
		}
	}
}
