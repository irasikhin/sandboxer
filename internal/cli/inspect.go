package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/backend"
	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/sandbox"
)

// sessionStates and allSessionStates are the session-enumeration seams for
// list and doctor — per project and host-wide — overridable in tests so the
// STATE column renders without a real engine.
var (
	sessionStates    = backend.SessionStates
	allSessionStates = backend.AllSessionStates
)

func init() {
	register(newListCmd)
	register(newShowCmd)
}

// baseOnly resolves the base for commands that don't need a specific slug.
func baseOnly(src string) (*sandbox.Base, error) {
	root := firstNonEmpty(src, getwd())
	if !sandbox.RunEnvExists(root) {
		return nil, fmt.Errorf("no sandboxes for %s (create one: sandboxer create <slug>)", root)
	}
	return sandbox.ResolveBase(root)
}

// listFmt is the per-project row layout; listAllFmt is the host-wide one, with
// a PROJECT column whose width is passed in (a '*' width arg) so the paths line
// up whether or not they were truncated. The ID column takes its width the same
// way, from sandbox.IDLen, so the handle the table prints and the one commands
// accept can never drift apart.
const (
	listFmt    = "%-2s %-*s %-16s %-9s %s\n"
	listAllFmt = "%-2s %-*s %-*s %-16s %-9s %s\n"
)

// Column budget for the host-wide table: fixedListCols is what listAllFmt
// spends on everything except PROJECT (the one variable column), and
// minProjectCol is the floor PROJECT keeps when a narrow terminal cannot
// pay for the full path.
const (
	fixedListCols = 37
	minProjectCol = 20
)

func newListCmd() *cobra.Command {
	var src string
	var wide, asJSON bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"status"},
		Short:   "List sandboxes and their status, across every project on this host",
		Long: `List sandboxes and their status.

The listing is HOST-WIDE by default: every project sandboxer holds state for,
not just the one in the current directory. Sandboxes are meant to run in
parallel, and the ones worth being reminded of are exactly the ones in a repo
you are not standing in — a stopped session, a finished agent, or a leftover
whose project directory is gone.

The ID column is each sandbox's host-wide handle: commands that take a slug
(rm, enter, exec, show, path, stop, ...) take an id — or any unambiguous
prefix of one — instead, and act on that sandbox in its own project without a
cd or --src. That is the only way to reach a sandbox whose project directory
is gone (the "!" rows).

Pass --src <path> to narrow the listing back to one project.
--json emits the same rows as machine-readable objects (full paths,
no truncation) for scripts and CI.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if src != "" {
				base, err := baseOnly(src)
				if err != nil {
					return err
				}
				if asJSON {
					states := map[string]map[string]string{base.Dir: projectSessionStates(base.Dir)}
					return writeListJSON(cmd.OutOrStdout(), []sandbox.Project{{Base: base}}, states)
				}
				printList(cmd, base, wide)
				return nil
			}
			if asJSON {
				return writeListJSON(cmd.OutOrStdout(), sandbox.Projects(), hostSessionStates())
			}
			printAll(cmd, sandbox.Projects(), wide)
			return nil
		},
	}
	cmd.Flags().StringVar(&src, "src", "", "list only this project (default: every project on this host)")
	cmd.Flags().BoolVarP(&wide, "wide", "w", false, "show full sandbox names and project paths (no truncation)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit machine-readable JSON instead of the table")
	return cmd
}

// listEntry is one sandbox in `list --json`. Paths are absolute and nothing is
// truncated: machine output is for reading, not for fitting a terminal.
type listEntry struct {
	ID          string `json:"id"`
	Project     string `json:"project"`
	Sandbox     string `json:"sandbox"`
	State       string `json:"state"`
	Image       string `json:"image,omitempty"`
	Active      bool   `json:"active,omitempty"`
	ProjectGone bool   `json:"projectGone,omitempty"`
}

// writeListJSON renders the listing as a JSON array — an empty host is [],
// never a hint string. Active is each project's own `use` pointer (the text
// table narrows it to the cwd project; a machine reading every project wants
// every pointer).
func writeListJSON(out io.Writer, projects []sandbox.Project, states map[string]map[string]string) error {
	entries := []listEntry{}
	for _, p := range projects {
		cur := p.Current()
		for _, slug := range p.Agents() {
			entries = append(entries, listEntry{
				ID:          p.ID(slug),
				Project:     p.Src,
				Sandbox:     slug,
				State:       sessionState(states[p.Dir], slug),
				Image:       imageLabel(loadStoredProfile(p.Base, slug)),
				Active:      slug != "" && slug == cur,
				ProjectGone: p.Gone,
			})
		}
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(entries)
}

func printList(cmd *cobra.Command, base *sandbox.Base, wide bool) {
	out := cmd.OutOrStdout()
	cur := base.Current()
	states := projectSessionStates(base.Dir)
	fmt.Fprintf(out, listFmt, "", sandbox.IDLen, "ID", "SANDBOX", "STATE", "IMAGE")
	for _, slug := range base.Agents() {
		marker := ""
		if slug == cur {
			marker = "*"
		}
		slugDisp := slug
		if !wide {
			slugDisp = truncate(slug, 16)
		}
		fmt.Fprintf(out, listFmt,
			marker, sandbox.IDLen, base.ID(slug), slugDisp, sessionState(states, slug),
			imageLabel(loadStoredProfile(base, slug)))
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "* = active (use). enter <s> | exec <s> -- cmd | show [s] | path [s] | rm <s>")
}

// allRow is one rendered host-wide row. The rows are collected before printing
// so the PROJECT column can be sized to what is actually there — and so an empty
// host prints a hint instead of a header with nothing under it.
type allRow struct {
	marker, id, project, slug, state, image string
}

// printAll renders the host-wide listing: every project's sandboxes in one
// table, the current directory's project first and the rest by path. The session
// STATE comes from ONE sweep per engine (not one probe per project), so the cost
// does not grow with the number of repos.
func printAll(cmd *cobra.Command, projects []sandbox.Project, wide bool) {
	out := cmd.OutOrStdout()
	states := hostSessionStates()
	wd, _ := filepath.Abs(getwd())
	longest := len("PROJECT")
	var rows []allRow
	var hasCur, hasGone bool
	for _, p := range orderProjects(projects, wd) {
		// The active marker is the CURRENT project's only: it answers "what does
		// a bare enter/exec hit", and a bare command hits nothing anywhere else.
		// Another project's `use` choice becomes visible once you cd there.
		cur := ""
		if p.Src == wd {
			cur = p.Current()
		}
		project := projectPath(p.Src)
		// Runes, not bytes: fmt's %-*s pads by rune count, and a left-truncated
		// path carries a 3-byte "…".
		if n := utf8.RuneCountInString(project); n > longest {
			longest = n
		}
		for _, slug := range p.Agents() {
			marker := ""
			switch {
			case p.Gone:
				marker, hasGone = "!", true
			case slug == cur && slug != "":
				marker, hasCur = "*", true
			}
			slugDisp := slug
			if !wide {
				slugDisp = truncate(slug, 16)
			}
			rows = append(rows, allRow{
				marker: marker, id: p.ID(slug), project: project, slug: slugDisp,
				state: sessionState(states[p.Dir], slug),
				image: imageLabel(loadStoredProfile(p.Base, slug)),
			})
		}
	}
	if len(rows) == 0 {
		fmt.Fprintln(out, "no sandboxes on this host (create one: sandboxer create <slug>)")
		return
	}
	width := projectWidth(longest, outWidth(out), wide)
	fmt.Fprintf(out, listAllFmt, "", sandbox.IDLen, "ID", width, "PROJECT", "SANDBOX", "STATE", "IMAGE")
	for _, r := range rows {
		fmt.Fprintf(out, listAllFmt, r.marker, sandbox.IDLen, r.id,
			width, truncateLeft(r.project, width), r.slug, r.state, r.image)
	}
	fmt.Fprintln(out)
	// Only the markers actually in the table get explained — a legend for a
	// symbol that is not there reads as a warning that is not there either.
	var legend []string
	if hasCur {
		legend = append(legend, "* = active (use) in the current project.")
	}
	if hasGone {
		legend = append(legend, "! = the project directory is gone (state left behind; rm <id> clears it).")
	}
	legend = append(legend,
		"ID reaches a sandbox in ANY project: rm <id> | enter <id> | show <id> (a unique prefix is enough).",
		"A bare slug means the current project — cd there, or pass --src <path> (narrows this list too).")
	for _, l := range legend {
		fmt.Fprintln(out, l)
	}
}

// orderProjects puts the project the user is standing in first — its sandboxes
// are the ones the bare enter/exec/rm act on — and leaves the rest in Projects'
// path order.
func orderProjects(projects []sandbox.Project, wd string) []sandbox.Project {
	for i, p := range projects {
		if p.Src != wd {
			continue
		}
		ordered := make([]sandbox.Project, 0, len(projects))
		ordered = append(ordered, p)
		ordered = append(ordered, projects[:i]...)
		return append(ordered, projects[i+1:]...)
	}
	return projects
}

// projectPath renders a project root for the PROJECT column: the home
// directory folded to ~. Never shortened here — how much of it fits is the
// table's decision (projectWidth), not the path's.
func projectPath(src string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if src == home {
			return "~"
		}
		if rest, ok := strings.CutPrefix(src, home+string(filepath.Separator)); ok {
			return "~" + string(filepath.Separator) + rest
		}
	}
	return src
}

// projectWidth decides how wide the PROJECT column may be. The default is the
// full longest path: a column that answers "which repo" but not "where" is the
// one thing this column exists for, so it is cut back only when a real terminal
// genuinely cannot fit it — and then only to what is left once the fixed
// columns are paid for, never below minProjectCol. term <= 0 means the output
// is not a terminal at all (a pipe, a file, a test buffer): nothing is
// truncated, because something is READING this. --wide keeps the full path
// unconditionally.
func projectWidth(longest, term int, wide bool) int {
	if wide || term <= 0 {
		return longest
	}
	room := term - fixedListCols
	if room < minProjectCol {
		room = minProjectCol
	}
	if longest > room {
		return room
	}
	return longest
}

// outWidth reports the terminal width behind a command's output stream, or 0
// when it is not a terminal (a pipe, a redirect, a test buffer) — deriving it
// from the writer the table actually prints to, so capturing the output can
// never truncate it.
func outWidth(out io.Writer) int {
	if f, ok := out.(*os.File); ok {
		return terminalWidth(f)
	}
	return 0
}

// truncateLeft shortens a path to n runes from the LEFT, keeping the tail that
// tells two checkouts apart.
func truncateLeft(s string, n int) string {
	r := []rune(s)
	if len(r) <= n || n < 1 {
		return s
	}
	return "…" + string(r[len(r)-n+1:])
}

// hostSessionStates maps baseDir → slug → session status for every project on
// the host, from one sweep per installed runner (a profile's `backend:` may
// have put sessions on either microVM runner). Best-effort like
// projectSessionStates: a runner problem drops that runner's answers only.
func hostSessionStates() map[string]map[string]string {
	all := map[string]map[string]string{}
	for _, engine := range backendSweepEngines(config.LoadDefaults()) {
		byBase, err := allSessionStates(engine)
		if err != nil {
			continue
		}
		for base, st := range byBase {
			if all[base] == nil {
				all[base] = make(map[string]string, len(st))
			}
			mergeStates(all[base], st)
		}
	}
	return all
}

// mergeStates folds one runner's slug→status answers into dst, where a
// "running" verdict wins over another runner's leftover record.
func mergeStates(dst, src map[string]string) {
	for slug, s := range src {
		if cur, ok := dst[slug]; !ok || cur != "running" {
			dst[slug] = s
		}
	}
}

// projectSessionStates returns slug→session status for the project's
// persistent sessions, probing every installed runner (a profile's `backend:`
// may have put sessions on either). Best-effort: any runner problem only
// drops that runner's answers, so the listing shows dashes instead of failing
// on a runner-less host.
func projectSessionStates(baseDir string) map[string]string {
	var states map[string]string
	for _, engine := range backendSweepEngines(config.LoadDefaults()) {
		st, err := sessionStates(engine, baseDir)
		if err != nil {
			continue
		}
		if states == nil {
			states = make(map[string]string, len(st))
		}
		// A "running" verdict wins over another engine's leftover record.
		mergeStates(states, st)
	}
	return states
}

// sessionState folds a session's machine status into the STATE column:
// "running" stays, any other recorded status reads "stopped", and a sandbox
// without a session machine shows "-".
func sessionState(states map[string]string, slug string) string {
	st, ok := states[slug]
	switch {
	case !ok:
		return "-"
	case st == "running":
		return "running"
	default:
		return "stopped"
	}
}

func newShowCmd() *cobra.Command {
	var f commonFlags
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show [slug]",
		Short: "Show the resolved profile and session state for a sandbox",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if inContainer() {
				fmt.Fprintln(out, "== profile ==")
				dumpFile(out, "/run/sandboxer/profile.json")
				return nil
			}
			t, err := resolveTarget(f, posArg(args))
			if err != nil {
				return err
			}
			rtShow, rtErr := t.runtime(f)
			if rtErr != nil {
				return rtErr
			}
			if asJSON {
				return writeShowJSON(out, t, rtShow)
			}
			fmt.Fprintln(out, configLine(rtShow, t.slug, t.profile, backendLabel(rtShow)))
			fmt.Fprintf(out, "== profile (%s) ==\n", t.slug)
			if !dumpFile(out, t.base.ProfileJSONPath(t.slug)) {
				fmt.Fprintln(out, "(no profile)")
			}
			printSourcesBlock(out, t)
			printPortsBlock(out, rtShow, sessionStatus(t, rtShow))
			printSessionBlock(out, t, rtShow)
			return nil
		},
	}
	bindExisting(cmd, &f)
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit machine-readable JSON instead of the sectioned text")
	return cmd
}

// showSource is one resolved source in `show --json` — the same facts srcLine
// prints, addressable by key.
type showSource struct {
	Repo    string   `json:"repo"`
	Branch  string   `json:"branch"`
	Include []string `json:"include,omitempty"`
	Path    string   `json:"path"`
	Adopted bool     `json:"adopted,omitempty"`
	// Git is the source's git-dir share ("ro"/"rw"), absent for the default
	// where git does not enter the sandbox; GitDir is the host path it shares.
	Git    string `json:"git,omitempty"`
	GitDir string `json:"gitDir,omitempty"`
	// Link is where an adopted source sits inside the sandbox directory (a
	// symlink to Path). Absent for a managed source, whose Path is already
	// there — so a consumer can tell the two apart without string surgery.
	Link string `json:"link,omitempty"`
}

// showSession is the session verdict in `show --json`. Fresh is a tri-state:
// absent when freshness could not be judged (no engine, cold image pin),
// false with StaleWhy naming what went stale.
type showSession struct {
	Container string `json:"container"`
	State     string `json:"state"` // none | running | stopped | unknown
	Fresh     *bool  `json:"fresh,omitempty"`
	StaleWhy  string `json:"staleWhy,omitempty"`
}

// writeShowJSON renders everything show's text sections say as one object:
// the stored resolved profile verbatim, the recorded sources, the session
// verdict.
func writeShowJSON(out io.Writer, t *target, rt config.Runtime) error {
	profile := json.RawMessage("null")
	if data, err := os.ReadFile(t.base.ProfileJSONPath(t.slug)); err == nil && json.Valid(data) {
		profile = json.RawMessage(data)
	}
	sources := []showSource{}
	for _, s := range t.base.Srcs(t.slug) {
		e := showSource{
			Repo: s.RepoRoot, Branch: s.Branch, Include: s.Include,
			Path: s.Path, Adopted: !s.Managed, Link: s.Link,
		}
		if config.GitShared(s.Git) && !noGit() {
			e.Git, e.GitDir = s.Git, s.GitDir
		}
		sources = append(sources, e)
	}
	session := sessionStatus(t, rt)
	doc := struct {
		Slug    string          `json:"slug"`
		Backend string          `json:"backend"`
		Profile json.RawMessage `json:"profile"`
		Sources []showSource    `json:"sources"`
		Ports   []showPort      `json:"ports,omitempty"`
		Session showSession     `json:"session"`
	}{
		Slug: t.slug, Backend: rt.Backend, Profile: profile,
		Sources: sources, Ports: showPorts(rt, session), Session: session,
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

// showPort is one published forward in `show --json`: what the config
// publishes, plus whether the machine that is RUNNING actually carries it —
// a forward lives in the create argv, so a session created before the port was
// configured has none however the config reads.
type showPort struct {
	Bind  string `json:"bind"`
	Host  int    `json:"host"`
	Guest int    `json:"guest"`
	Proto string `json:"proto"`
	URL   string `json:"url"`
	Live  bool   `json:"live"`
}

// portsLive reports whether the forwards in the resolved config are the ones
// the running session was created with: only a running AND fresh session
// carries them (a stale one predates the current create argv, a stopped or
// absent one has no listener at all).
func portsLive(s showSession) bool {
	return s.State == "running" && s.Fresh != nil && *s.Fresh
}

// showPorts projects the resolved forwards for `show --json`.
func showPorts(rt config.Runtime, s showSession) []showPort {
	live := portsLive(s)
	out := make([]showPort, 0, len(rt.Ports))
	for _, p := range rt.Ports {
		out = append(out, showPort{
			Bind: p.Bind, Host: p.Host, Guest: p.Guest, Proto: p.Proto,
			URL: fmt.Sprintf("http://%s:%d/", p.Bind, p.Host), Live: live,
		})
	}
	return out
}

// printPortsBlock renders show's "== ports ==" lines: the forwards the config
// publishes and the URL each one answers on — but only when the running
// session actually has them. A configured port beside a stale or stopped
// session is precisely the state where the config looks right and the browser
// says "unable to connect", so it is named here rather than left to be
// discovered in a browser.
func printPortsBlock(out io.Writer, rt config.Runtime, s showSession) {
	fmt.Fprintln(out, "== ports ==")
	if len(rt.Ports) == 0 {
		fmt.Fprintln(out, "(none — no inbound path into the sandbox)")
		return
	}
	for _, p := range rt.Ports {
		if portsLive(s) {
			fmt.Fprintf(out, "%s — open http://%s:%d/\n", p, p.Bind, p.Host)
			continue
		}
		fmt.Fprintf(out, "%s — NOT live yet (session %s; a forward only exists in a machine created with it)\n",
			p, portsSessionWhy(s))
	}
}

// portsSessionWhy names, in a few words, why the forwards are not up.
func portsSessionWhy(s showSession) string {
	switch {
	case s.State == "none":
		return "does not exist — create it with `sandboxer enter`"
	case s.State == "unknown":
		return "state is unknown"
	case s.State != "running":
		return s.State + " — start it with `sandboxer enter`"
	case s.Fresh != nil && !*s.Fresh:
		return "is stale: " + s.StaleWhy + " — rebuild with `sandboxer stop` then `sandboxer enter`"
	default:
		return s.State
	}
}

// printSourcesBlock renders show's "== sources ==" lines: the RESOLVED sources
// — one per repo, with its branch, any include narrowing and the host path of
// the worktree — as recorded at the last sync. The profile block above shows
// what the config ASKS for; this shows what the sandbox actually got, which is
// where the paths (and any adoption) become visible. Print with 'sandboxer
// path' to get a bare path back.
func printSourcesBlock(out io.Writer, t *target) {
	fmt.Fprintln(out, "== sources ==")
	srcs := t.base.Srcs(t.slug)
	if len(srcs) == 0 {
		fmt.Fprintln(out, "(none recorded — enter the sandbox once)")
		return
	}
	for _, s := range srcs {
		fmt.Fprintln(out, srcLine(s))
	}
}

// sessionStatus computes the session verdict both renderings share: the
// deterministic container name, its current state, and whether the session is
// still fresh — the config hash recorded at create time matches the profile
// AND the container still runs the engine's current image — or the next
// persistent enter would recreate it (stale, naming which of the two went).
// Best-effort: with no engine the state is unknown, never an error.
func sessionStatus(t *target, rt config.Runtime) showSession {
	s := showSession{Container: backend.SessionName(t.slug, t.base.Dir)}
	engine, err := backend.ResolveEngine(rt.Backend, config.LoadDefaults())
	if err != nil {
		s.State = "unknown"
		return s
	}
	info := backendInspectSession(engine, s.Container)
	if !info.Exists {
		s.State = "none"
		return s
	}
	s.State = "stopped"
	if info.Running {
		s.State = "running"
	}
	fresh := new(bool)
	switch o, ok := sessionHashOpts(t, rt, engine); {
	case !ok:
	case info.Hash != backendWantHash(o):
		s.Fresh, s.StaleWhy = fresh, "the profile changed"
	case !backend.ImageFresh(info.ImageID, backendImageID(engine, o.Image)):
		s.Fresh, s.StaleWhy = fresh, "the image was rebuilt"
	default:
		*fresh = true
		s.Fresh = fresh
	}
	return s
}

// printSessionBlock renders show's "== session ==" lines from the shared
// sessionStatus verdict.
func printSessionBlock(out io.Writer, t *target, rt config.Runtime) {
	fmt.Fprintln(out, "== session ==")
	s := sessionStatus(t, rt)
	fmt.Fprintf(out, "container: %s\n", s.Container)
	switch {
	case s.State == "unknown":
		fmt.Fprintln(out, "state: unknown (no microVM runner installed)")
	case s.State == "none":
		fmt.Fprintln(out, "state: none (a persistent enter creates it)")
	case s.Fresh == nil:
		fmt.Fprintf(out, "state: %s\n", s.State)
	case !*s.Fresh:
		fmt.Fprintf(out, "state: %s (stale — %s; re-enter recreates it)\n", s.State, s.StaleWhy)
	default:
		fmt.Fprintf(out, "state: %s (fresh)\n", s.State)
	}
}

// sessionHashOpts assembles the RunOpts SessionWantHash needs to recompute
// the session's config hash — the same fields enter passes, minus the stdio.
// ok=false when the image cannot be resolved, leaving freshness unjudged. The
// image resolve gets NO engine on purpose: show is read-only, so a cold
// "latest" pin must never launch a resolver container or stamp the pins cache
// from here — a warm stamp still resolves, a cold one just skips the
// freshness verdict. A failed mount resolve (include pattern matching
// nothing) degrades the same way: show stays read-only and diagnostic, the
// hard error belongs to enter/exec.
func sessionHashOpts(t *target, rt config.Runtime, engine string) (backend.RunOpts, bool) {
	image, spec, err := resolveImage(t.profile, io.Discard)
	if err != nil {
		return backend.RunOpts{}, false
	}
	mp, err := t.mounts()
	if err != nil {
		return backend.RunOpts{}, false
	}
	return backend.RunOpts{
		Engine: engine, Image: image, Spec: spec,
		Dest: t.base.SandboxDir(t.slug), Slug: t.slug, BaseDir: t.base.Dir,
		MountDest: mp.Dest,
		MountGen:  mp.Gen,
		MountIDs:  mp.IDs,
		SrcMounts: mp.Src,
		GitMounts: mp.Git,
		HomeDir:   t.base.HomeDir(t.slug),
		DestGen:   t.base.Gen(t.slug),
		AuthEnv:   hostAuthEnv(t.profile),
		RT:        rt, Profile: t.profile,
		ProfileJSONPath: t.base.ProfileJSONPath(t.slug),
		Mem:             rt.Mem, CPU: rt.CPU,
		NoEgress: noEgress(),
	}, true
}

func dumpFile(out interface{ Write([]byte) (int, error) }, path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	_, _ = out.Write(data)
	if len(data) > 0 && data[len(data)-1] != '\n' {
		_, _ = out.Write([]byte("\n"))
	}
	return true
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
