package backend

import (
	"encoding/json"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/irasikhin/sandboxer/internal/registry"
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
// it resume — `claude --continue` — once relaunched there. The relaunch itself
// is automated for cataloged agents: capture records WHICH registry agent ran
// in each pane (TmuxPane.Agent), and the restore types the agent's resume argv
// into the rebuilt pane's shell (see TmuxRestoreScript).

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
	// Agent names the registry agent that was running in this pane at capture
	// time ("" = none detected). The restore relaunches it via the agent's
	// resume argv, typed into the rebuilt pane's shell. Detection walks the
	// pane's process tree (resolveAgents), not pane_current_command, because a
	// node-based agent's foreground comm may read "node".
	Agent string `json:"agent,omitempty"`
	// pid is the pane's root process at capture time — transient input to the
	// agent detection, never persisted (meaningless once the container is gone).
	pid int
}

// tmuxSocket is the in-container server socket enter attaches (see D4 /
// tmuxEnterArgs): every capture/restore command targets `tmux -L sandboxer`. A
// var, not a const, ONLY so a real-tmux round-trip test can redirect it to a
// throwaway socket instead of a live session; it is never reassigned at runtime.
var tmuxSocket = "sandboxer"

// captureFormat lists one line per pane across every session and window on the
// server, tab-separated so each field keeps its slot even when empty. The order
// is session, window (index/name/active/layout), then pane (index/active/
// path/command/pid) — parseTmuxState groups by the leading session and window
// keys. The window fields repeat for each of the window's panes; the parser
// takes the first. Tab is the separator because a path may contain spaces but
// virtually never a tab, and tmux emits the format verbatim. pane_pid rides
// last so the parser's 9-field floor keeps accepting output without it.
const captureFormat = "#{session_name}\t#{window_index}\t#{window_name}\t#{window_active}\t#{window_layout}\t" +
	"#{pane_index}\t#{pane_active}\t#{pane_current_path}\t#{pane_current_command}\t#{pane_pid}"

// CaptureTmuxState reads the live tmux server inside the session container and
// returns its sessions, ready to persist. It returns nil when the server holds
// nothing worth restoring — no server running, or a failure reaching it —
// mirroring SessionIdle's positive-finding stance: an engine or tmux error is
// "nothing to capture", never a fabricated layout. The caller persists a nil
// result as an empty/reset state, not a crash.
func CaptureTmuxState(engine, name string) []TmuxSession {
	out, err := guestExec(engine, name,
		"tmux", "-L", tmuxSocket, "list-panes", "-a", "-F", captureFormat).Output()
	if err != nil {
		return nil
	}
	sessions := parseTmuxState(string(out))
	tagAgents(engine, name, sessions)
	return sessions
}

// tagAgents records which registry agent ran in each captured pane, from one
// process listing of the still-running container. Best-effort BY DESIGN: a
// failed listing leaves every Agent empty and the layout capture stands — a
// detection failure must never cost the user their windows, and an empty Agent
// only degrades the restore to a plain shell (never a wrong relaunch).
func tagAgents(engine, name string, sessions []TmuxSession) {
	if !anyPanePid(sessions) {
		return
	}
	out, err := guestExec(engine, name, "ps", "-eo", "pid=,ppid=,args=").Output()
	if err != nil {
		return
	}
	resolveAgents(sessions, parsePS(string(out)), registry.Bins())
}

// anyPanePid reports whether the capture carried at least one pane pid — an
// older tmux emitting fewer fields yields none, and then the ps exec is skipped.
func anyPanePid(sessions []TmuxSession) bool {
	for _, s := range sessions {
		for _, w := range s.Windows {
			for _, p := range w.Panes {
				if p.pid != 0 {
					return true
				}
			}
		}
	}
	return false
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
		pid := 0
		if len(f) >= 10 {
			pid, _ = strconv.Atoi(f[9])
		}

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
			pid:     pid,
		})
	}
	return sessions
}

// proc is one row of the container's process listing (parsePS).
type proc struct {
	pid, ppid int
	argv      []string
}

// parsePS turns `ps -eo pid=,ppid=,args=` output into a pid-keyed process
// table. Fields split on whitespace: the first two must be numeric, the rest is
// the argv (ps joins argv words with single spaces — an argv word that itself
// contains a space splits wrong, which at worst misses a match, never invents
// one). Malformed rows are skipped.
func parsePS(out string) map[int]proc {
	procs := map[int]proc{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		procs[pid] = proc{pid: pid, ppid: ppid, argv: f[2:]}
	}
	return procs
}

// interpreterBases are the script-runner comms an agent CLI may execute under:
// a nix/npm bin is a node-shebang script, so its cmdline reads
// `/nix/…/bin/node /nix/…/bin/claude …` — argv[0] names the interpreter and
// argv[1] the agent bin the user actually invoked.
var interpreterBases = map[string]bool{"node": true, "bun": true, "deno": true}

// matchAgent names the registry agent a process is, or "" when it is none.
// Deliberately precise — only argv[0] (a compiled agent, or a runtime that
// rewrote its title to the bin) or an interpreter's argv[1] (the shebang
// shape) count. Never "any argv word": `less claude` or `git log -- claude`
// must not read as a running agent, because a false match auto-runs a command
// in the user's shell.
func matchAgent(p proc, bins map[string]string) string {
	if len(p.argv) == 0 {
		return ""
	}
	if name, ok := bins[path.Base(p.argv[0])]; ok {
		return name
	}
	base := path.Base(p.argv[0])
	if len(p.argv) >= 2 && (interpreterBases[base] || strings.HasPrefix(base, "python")) {
		if name, ok := bins[path.Base(p.argv[1])]; ok {
			return name
		}
	}
	return ""
}

// resolveAgents stamps TmuxPane.Agent for every captured pane: a BFS from the
// pane's pid down the child tree, nearest-first, first match wins — that is
// the process the user launched, not the agent's own helper children (MCP
// servers, node workers). The pane pid itself is level zero, so an `exec
// claude` pane (the agent replaced the shell) matches too. Ties within a level
// break by pid order for determinism.
func resolveAgents(sessions []TmuxSession, procs map[int]proc, bins map[string]string) {
	children := map[int][]int{}
	for pid, p := range procs {
		children[p.ppid] = append(children[p.ppid], pid)
	}
	for _, c := range children {
		sort.Ints(c)
	}
	for si := range sessions {
		for wi := range sessions[si].Windows {
			panes := sessions[si].Windows[wi].Panes
			for pi := range panes {
				if panes[pi].pid != 0 {
					panes[pi].Agent = nearestAgent(panes[pi].pid, procs, children, bins)
				}
			}
		}
	}
}

// nearestAgent walks pid's subtree breadth-first and returns the first
// registry-agent match. The seen guard tolerates a cyclic ppid chain from a
// torn ps snapshot (a pid reused mid-listing) — degrade to "", never loop.
func nearestAgent(pid int, procs map[int]proc, children map[int][]int, bins map[string]string) string {
	level := []int{pid}
	seen := map[int]bool{pid: true}
	for len(level) > 0 {
		var next []int
		for _, id := range level {
			if p, ok := procs[id]; ok {
				if name := matchAgent(p, bins); name != "" {
					return name
				}
			}
			for _, c := range children[id] {
				if !seen[c] {
					seen[c] = true
					next = append(next, c)
				}
			}
		}
		level = next
	}
	return ""
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
// resume maps a pane's recorded Agent to its resume surface: right after such
// a pane is created (it is the window's active pane at that moment, so no
// pane-index arithmetic), the script TYPES the command into the pane's shell —
// send-keys -l plus a separate Enter — instead of making it the pane's
// command, so the shell survives the agent's exit exactly like a hand-typed
// run. The keys queue in the pty buffer even before the shell finishes
// startup (the tmux-resurrect approach). nil resume restores layout only.
//
// Which command is typed depends on ambiguity: an agent's Last command resumes
// the latest conversation OF THE PANE'S DIRECTORY, which is exact while that
// directory held one such pane — but when the capture recorded SEVERAL panes
// of the same agent in the same directory (two claudes on one worktree, in
// whatever windows or sessions), Last would open the same conversation in
// every one of them. Those panes get the agent's Pick command instead — the
// interactive picker listing exactly that directory's conversations — because
// the specific conversation cannot be identified from outside the process
// (claude neither keeps its transcript fd open nor exports a session id).
//
// nil/empty sessions yields the plain attach, so a caller can use this
// unconditionally.
func TmuxRestoreScript(sessions []TmuxSession, attach string, resume map[string]registry.ResumeSpec) string {
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

	// crowd counts, per (agent, directory), how many panes across the WHOLE
	// capture ran that agent there — every tmux session shares the one sandbox
	// $HOME, so ambiguity spans sessions and windows alike.
	crowd := map[string]int{}
	for _, s := range sessions {
		for _, w := range s.Windows {
			for _, p := range w.Panes {
				if p.Agent != "" {
					crowd[p.Agent+"\x00"+p.Path]++
				}
			}
		}
	}
	// resumeArgv picks the command a pane gets typed into it: the picker when
	// this agent+directory pair is crowded, or when the agent ships ONLY a
	// picker (no "continue the latest" flag) — one keystroke to the same
	// conversation beats restoring a bare shell; else Last.
	resumeArgv := func(p TmuxPane) []string {
		spec, ok := resume[p.Agent]
		if p.Agent == "" || !ok {
			return nil
		}
		if crowded := crowd[p.Agent+"\x00"+p.Path] > 1; (crowded || len(spec.Last) == 0) && len(spec.Pick) > 0 {
			return spec.Pick
		}
		return spec.Last
	}
	// sendResume types the pane's resume command into the window's just-created
	// (= active) pane. -l sends the command literally — a resume word can never
	// be read as a tmux key name — which is why Enter is its own send-keys.
	sendResume := func(b *strings.Builder, tmux, target string, p TmuxPane) {
		argv := resumeArgv(p)
		if len(argv) == 0 {
			return
		}
		b.WriteString("  " + tmux + "send-keys -t " + target + " -l " + shquote(shjoin(argv)) + "\n")
		b.WriteString("  " + tmux + "send-keys -t " + target + " Enter\n")
	}

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
			if len(w.Panes) > 0 {
				sendResume(&b, tmux, target, w.Panes[0])
			}
			if len(w.Panes) > 1 {
				for _, p := range w.Panes[1:] {
					b.WriteString("  " + tmux + "split-window -t " + target + cflag(p.Path) + "\n")
					sendResume(&b, tmux, target, p)
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

// SyncSessionState refreshes the saved layout after an attached enter returns,
// so the freshest state is on disk even if the container later dies without a
// graceful stop/recreate (host reboot, engine restart). It deliberately does
// NOT share SaveSessionState's refuse-empty guard — there, at stop/recreate,
// an empty capture usually means "container already gone"; here the container
// just served the attach, so emptiness has a second, positive reading:
//
//	non-empty capture             → save it (a detach left the session live)
//	empty capture + SessionIdle   → save [] — the user deliberately ended every
//	                                session, so the next enter starts fresh,
//	                                exactly what the exit banner promises
//	engine/tmux error             → keep the last good save
//
// Best-effort like every capture: never blocks or fails the enter that ran it.
func SyncSessionState(engine, name, statePath string) {
	if statePath == "" {
		return
	}
	if s := CaptureTmuxState(engine, name); len(s) > 0 {
		_ = WriteTmuxState(statePath, s)
		return
	}
	if SessionIdle(engine, name) {
		_ = WriteTmuxState(statePath, nil)
	}
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

// bareWordRe is the alphabet a shjoin word may keep unquoted — anything else
// is shquote'd so a hostile registry resume word can never break out of the
// command line typed into the pane.
var bareWordRe = regexp.MustCompile(`^[A-Za-z0-9@%_+=:,./-]+$`)

// shjoin renders an argv as one shell command line for the pane's shell:
// plain words stay bare (so `claude --continue` reads as typed by hand),
// anything else is single-quoted per word.
func shjoin(argv []string) string {
	words := make([]string, len(argv))
	for i, w := range argv {
		if bareWordRe.MatchString(w) {
			words[i] = w
		} else {
			words[i] = shquote(w)
		}
	}
	return strings.Join(words, " ")
}
