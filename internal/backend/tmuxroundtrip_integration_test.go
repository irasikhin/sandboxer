//go:build integration

package backend

import (
	"os/exec"
	"reflect"
	"testing"
)

// TestTmuxRoundTrip_RealTmux validates capture+restore against a REAL tmux
// server — no container, no engine, just `tmux -L <throwaway>` on an isolated
// socket. It builds a known layout, captures it with the exact format the
// backend uses, restores it with TmuxRestoreScript into a fresh server, and
// asserts the rebuilt layout matches the captured one. This proves the piece the
// stub-tmux unit test cannot: that real tmux's list-panes output parses, and
// that its real select-layout / base-index / -c behavior reproduces the geometry
// and working directories. Gated on `integration` (spawns processes) and skips
// cleanly when tmux/bash are absent.
func TestTmuxRoundTrip_RealTmux(t *testing.T) {
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not installed")
	}

	// Redirect capture/restore off the real `-L sandboxer` server onto a
	// throwaway socket, so this never touches a developer's live session.
	old := tmuxSocket
	tmuxSocket = "sbxtest-roundtrip"
	defer func() { tmuxSocket = old }()

	tmux := func(args ...string) *exec.Cmd {
		return exec.Command(tmuxBin, append([]string{"-L", tmuxSocket}, args...)...)
	}
	kill := func() { _ = tmux("kill-server").Run() }
	kill()
	defer kill()

	dirA, dirB := t.TempDir(), t.TempDir()
	must := func(c *exec.Cmd) {
		t.Helper()
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", c.Args, err, out)
		}
	}
	// session 'main': window 0 'edit' (two panes, cwds dirA/dirB), window 1
	// 'logs' (one pane, dirB); focus back on window 0.
	// Target windows by NAME, not index, so the setup does not assume base-index
	// 0 (the host/toolbox tmux uses base-index 1 — the very case the restore
	// handles at runtime).
	must(tmux("new-session", "-d", "-s", "main", "-n", "edit", "-c", dirA))
	must(tmux("split-window", "-t", "main:edit", "-c", dirB))
	must(tmux("new-window", "-t", "main", "-n", "logs", "-c", dirB))
	must(tmux("select-window", "-t", "main:edit"))

	capture := func() []TmuxSession {
		t.Helper()
		out, err := tmux("list-panes", "-a", "-F", captureFormat).Output()
		if err != nil {
			t.Fatalf("capture: %v", err)
		}
		return parseTmuxState(string(out))
	}

	captured := capture()
	assertLayout(t, "captured", captured)

	// Tear the server down and rebuild it from the captured state alone.
	kill()
	script := TmuxRestoreScript(captured, "main", nil)
	// The script ends in `new-session -A` (attach); with no TTY that final step
	// fails, but the rebuild ran first — so ignore the exit and re-capture.
	_ = exec.Command("bash", "-c", script).Run()

	restored := capture()
	assertLayout(t, "restored", restored)

	// The structural shape must survive the round trip exactly (window names and
	// order, pane counts and cwds, active window). Layout strings and per-pane
	// active flags are allowed to differ (client size / last-split focus), so
	// compare the load-bearing fields rather than DeepEqual on the whole.
	if !reflect.DeepEqual(shape(captured), shape(restored)) {
		t.Fatalf("round trip changed the session shape:\ncaptured %#v\nrestored %#v",
			shape(captured), shape(restored))
	}
}

// assertLayout checks the concrete layout the test built: one session 'main',
// windows edit(2 panes)/logs(1 pane), the edit panes in two DISTINCT dirs, and
// the active window being 'edit'. The pane cwds are compared as a set through
// tmux's own reported paths, so symlink resolution stays consistent.
func assertLayout(t *testing.T, label string, s []TmuxSession) {
	t.Helper()
	if len(s) != 1 || s[0].Name != "main" {
		t.Fatalf("%s: want one session 'main', got %#v", label, s)
	}
	w := s[0].Windows
	if len(w) != 2 {
		t.Fatalf("%s: want 2 windows, got %d", label, len(w))
	}
	if w[0].Name != "edit" || len(w[0].Panes) != 2 {
		t.Fatalf("%s: window0=%q panes=%d, want edit/2", label, w[0].Name, len(w[0].Panes))
	}
	if w[1].Name != "logs" || len(w[1].Panes) != 1 {
		t.Fatalf("%s: window1=%q panes=%d, want logs/1", label, w[1].Name, len(w[1].Panes))
	}
	if ai := activeWindow(w); ai != 0 {
		t.Fatalf("%s: active window should be ordinal 0 (edit), got %d", label, ai)
	}
	// The two edit panes must be in the two distinct dirs we opened them in.
	paths := map[string]bool{w[0].Panes[0].Path: true, w[0].Panes[1].Path: true}
	if len(paths) != 2 {
		t.Fatalf("%s: edit panes share a cwd, want two distinct: %#v", label, w[0].Panes)
	}
}

// shapeWindow / shape project a captured session onto just its round-trip-stable
// fields, so the before/after comparison ignores layout strings and pane-active
// flags that legitimately move.
type shapeWindow struct {
	Name  string
	Cwds  []string
	Focus int
}

func shape(sessions []TmuxSession) []shapeWindow {
	var out []shapeWindow
	for _, s := range sessions {
		for _, w := range s.Windows {
			cwds := make([]string, len(w.Panes))
			for j, p := range w.Panes {
				cwds[j] = p.Path
			}
			focus := 0
			if w.Active {
				focus = 1
			}
			out = append(out, shapeWindow{Name: w.Name, Cwds: cwds, Focus: focus})
		}
	}
	return out
}
