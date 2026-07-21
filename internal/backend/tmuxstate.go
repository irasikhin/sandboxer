package backend

import (
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// The session's user-facing shape — its tmux sessions, their windows and panes
// with layout and working directories — captured to the host so it OUTLIVES the
// container it ran in. The container is a disposable substrate; this is the
// session. Recorded before a container is replaced (recreate/stop) and replayed
// on the next attach, so a rebuild that a new mount forces no longer costs the
// user their windows. Only `rm`/`clean` delete the recorded state — a config or
// mount change replaces the container and restores this, it never discards it.
//
// What is captured is STRUCTURE, not live process state: the running program in
// a pane cannot be migrated across a container it is a process of, so a pane is
// restored as a shell in its old directory. An agent's own durable state (its
// conversation under the per-sandbox $HOME, which is a host mount) is what lets
// it resume — `claude --continue` — once relaunched there.

// TmuxSession is one captured tmux session on the in-container `-L sandboxer`
// server: its name and its windows, in order.
type TmuxSession struct {
	Name    string       `json:"name"`
	Windows []TmuxWindow `json:"windows"`
}

// TmuxWindow is one window of a captured session. Layout is tmux's own
// window_layout string, which encodes the exact pane geometry and is replayed
// verbatim via `select-layout`; Active marks the window that had focus.
type TmuxWindow struct {
	Name   string     `json:"name"`
	Layout string     `json:"layout"`
	Active bool       `json:"active"`
	Panes  []TmuxPane `json:"panes"`
}

// TmuxPane is one pane of a window. Path is its working directory (restored via
// `-c`); Command is the foreground process at capture time, kept only to name
// what the user should relaunch (a running process is never migrated).
type TmuxPane struct {
	Path    string `json:"path"`
	Command string `json:"command"`
	Active  bool   `json:"active"`
}

// tmuxSocket is the in-container server socket enter attaches (see D4 /
// tmuxEnterArgs): every capture/restore command targets `tmux -L sandboxer`. A
// var, not a const, ONLY so a real-tmux round-trip test can redirect it to a
// throwaway socket instead of a live session; it is never reassigned at runtime.
var tmuxSocket = "sandboxer"

// captureFormat lists one line per pane across every session and window on the
// server, tab-separated so each field keeps its slot even when empty. The order
// is session, window (index/name/active/layout), then pane (index/active/
// path/command) — parseTmuxState groups by the leading session and window keys.
// The window fields repeat for each of the window's panes; the parser takes the
// first. Tab is the separator because a path may contain spaces but virtually
// never a tab, and tmux emits the format verbatim.
const captureFormat = "#{session_name}\t#{window_index}\t#{window_name}\t#{window_active}\t#{window_layout}\t" +
	"#{pane_index}\t#{pane_active}\t#{pane_current_path}\t#{pane_current_command}"

// CaptureTmuxState reads the live tmux server inside the session container and
// returns its sessions, ready to persist. It returns nil when the server holds
// nothing worth restoring — no server running, or a failure reaching it —
// mirroring SessionIdle's positive-finding stance: an engine or tmux error is
// "nothing to capture", never a fabricated layout. The caller persists a nil
// result as an empty/reset state, not a crash.
func CaptureTmuxState(engine, name string) []TmuxSession {
	out, err := exec.Command(engine, "exec", name,
		"tmux", "-L", tmuxSocket, "list-panes", "-a", "-F", captureFormat).Output()
	if err != nil {
		return nil
	}
	return parseTmuxState(string(out))
}

// parseTmuxState turns the tab-separated capture into ordered sessions. Pure, so
// the grouping is unit-tested without an engine. Order is preserved as the
// server reported it (list-panes walks sessions then windows then panes); a
// malformed line — too few fields — is skipped rather than aborting the whole
// restore over one bad row.
func parseTmuxState(out string) []TmuxSession {
	var sessions []TmuxSession
	sessionAt := map[string]int{} // session name -> index in sessions
	windowAt := map[string]int{}  // "session\twindow" -> index in that session's windows
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 9 {
			continue
		}
		sName, wIndex, wName, wActive, wLayout := f[0], f[1], f[2], f[3], f[4]
		pActive, pPath, pCmd := f[6], f[7], f[8]

		si, ok := sessionAt[sName]
		if !ok {
			si = len(sessions)
			sessionAt[sName] = si
			sessions = append(sessions, TmuxSession{Name: sName})
		}
		wKey := sName + "\t" + wIndex
		wi, ok := windowAt[wKey]
		if !ok {
			wi = len(sessions[si].Windows)
			windowAt[wKey] = wi
			sessions[si].Windows = append(sessions[si].Windows, TmuxWindow{
				Name:   wName,
				Layout: wLayout,
				Active: wActive == "1",
			})
		}
		sessions[si].Windows[wi].Panes = append(sessions[si].Windows[wi].Panes, TmuxPane{
			Path:    pPath,
			Command: pCmd,
			Active:  pActive == "1",
		})
	}
	return sessions
}

// WriteTmuxState persists sessions as JSON at path (the host state file, outside
// the container). A nil capture writes an empty array, which restores as a fresh
// session — the intended reset when the user had already ended everything.
func WriteTmuxState(path string, sessions []TmuxSession) error {
	if sessions == nil {
		sessions = []TmuxSession{}
	}
	data, err := json.MarshalIndent(sessions, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// ReadTmuxState loads a persisted capture (nil when the file is absent or
// unreadable — no state to restore, which the caller treats as "attach fresh").
func ReadTmuxState(path string) []TmuxSession {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var sessions []TmuxSession
	if json.Unmarshal(data, &sessions) != nil {
		return nil
	}
	return sessions
}

// TmuxRestoreScript builds the in-container bash the attach runs INSTEAD of a
// bare `tmux new-session -A` when a saved layout exists: it rebuilds every saved
// session that is not already present, then attaches to attach (creating it if
// the saved set did not include it). Returned as a script for `bash -c`, the
// same shape tmuxEnterArgs uses, so nothing in the container needs to read the
// host state file — the whole layout is baked into the command here.
//
// The rebuild is idempotent by construction: each session is guarded by
// `has-session`, so a second terminal attaching, or a re-enter into a container
// whose sessions are already live, skips straight to the attach and never
// clobbers running work. Windows are placed at base-index + ordinal (base-index
// read from the server at run time, so a config that starts windows at 1 is
// honored), each pane opens in its captured directory, and a multi-pane window
// replays tmux's own layout string. select-layout is skipped for a lone pane
// (the layout of one pane is a no-op and tmux rejects a mismatched count).
//
// nil/empty sessions yields the plain attach, so a caller can use this
// unconditionally.
func TmuxRestoreScript(sessions []TmuxSession, attach string) string {
	tmux := "tmux -L " + shquote(tmuxSocket) + " "
	attachCmd := "exec " + tmux + "new-session -A -s " + shquote(attach)
	if len(sessions) == 0 {
		return attachCmd
	}

	var b strings.Builder
	// No `set -e`/`set -u`: a restore is best-effort — a failed tmux command must
	// never stop the script short of the final attach, which always yields a
	// working session. The base-index (B) is read PER SESSION from the window
	// new-session just created, NOT up front: the server — and the base-index its
	// config sets — does not exist until the first new-session, so an early
	// `show-options` (or `start-server`) reads 0 and every window target would
	// miss on a base-index-1 image (the toolbox's default). Real tmux caught this.

	for _, s := range sessions {
		if s.Name == "" || len(s.Windows) == 0 {
			continue
		}
		name := shquote(s.Name)
		b.WriteString("if ! " + tmux + "has-session -t " + shquote("="+s.Name) + " 2>/dev/null; then\n")
		for wi, w := range s.Windows {
			target := name + ":$((B+" + strconv.Itoa(wi) + "))"
			if wi == 0 {
				b.WriteString("  " + tmux + "new-session -d -s " + name + cflag(firstPath(w)) + "\n")
				// Read the ACTUAL base-index from the window new-session just made
				// (the server, and its config, did not exist until this line).
				b.WriteString("  B=$(" + tmux + "display-message -p -t " + name + " '#{window_index}' 2>/dev/null); B=${B:-0}\n")
				if w.Name != "" {
					b.WriteString("  " + tmux + "rename-window -t " + target + " " + shquote(w.Name) + "\n")
				}
			} else {
				win := "new-window -t " + target
				if w.Name != "" {
					win += " -n " + shquote(w.Name)
				}
				b.WriteString("  " + tmux + win + cflag(firstPath(w)) + "\n")
			}
			if len(w.Panes) > 1 {
				for _, p := range w.Panes[1:] {
					b.WriteString("  " + tmux + "split-window -t " + target + cflag(p.Path) + "\n")
				}
				if w.Layout != "" {
					b.WriteString("  " + tmux + "select-layout -t " + target + " " + shquote(w.Layout) + "\n")
				}
			}
		}
		if ai := activeWindow(s.Windows); ai >= 0 {
			b.WriteString("  " + tmux + "select-window -t " + name + ":$((B+" + strconv.Itoa(ai) + "))\n")
		}
		b.WriteString("fi\n")
	}
	// No trailing newline: the caller splices this as the then-branch of a
	// `command -v tmux` guard (`then <this>; else …`), and a trailing newline
	// before the `;` would be a shell syntax error.
	b.WriteString(attachCmd)
	return b.String()
}

// SaveSessionState captures the live tmux layout from the session container and
// persists it at statePath, so a later attach can restore it. It writes ONLY
// when there is a live layout to save: an idle or already-stopped container
// (nothing captured) leaves any earlier save intact — the recorded session is
// deleted only by rm/clean, never overwritten with emptiness by a routine
// replace. Returns whether it saved. Best-effort: a write error is swallowed,
// because losing a layout must never block the stop/recreate that triggered it.
func SaveSessionState(engine, name, statePath string) bool {
	if statePath == "" {
		return false
	}
	s := CaptureTmuxState(engine, name)
	if len(s) == 0 {
		return false
	}
	return WriteTmuxState(statePath, s) == nil
}

// firstPath returns the working directory to open a window's first pane in, or
// "" when the capture recorded none (let tmux use its default).
func firstPath(w TmuxWindow) string {
	if len(w.Panes) > 0 {
		return w.Panes[0].Path
	}
	return ""
}

// cflag renders a tmux `-c <dir>` fragment for a captured path, or "" to omit it
// (an empty path lets tmux choose the default rather than force one).
func cflag(path string) string {
	if path == "" {
		return ""
	}
	return " -c " + shquote(path)
}

// activeWindow returns the ordinal of the active window, or -1 when none is
// marked (leave tmux's own default focus).
func activeWindow(windows []TmuxWindow) int {
	for i, w := range windows {
		if w.Active {
			return i
		}
	}
	return -1
}

// shquote wraps s for safe inclusion in a single-quoted shell word, escaping any
// embedded single quote so it cannot end the quoted word. Session/window names come from the user's tmux
// and paths from the worktree, so every interpolated value is quoted — this is a
// correctness AND an injection guard for the generated restore script.
func shquote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
