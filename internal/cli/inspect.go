package cli

import (
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
	listFmt    = "%-2s %-*s %-16s %s\n"
	listAllFmt = "%-2s %-*s %-*s %-16s %s\n"
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
	var wide bool
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

Pass --src <path> to narrow the listing back to one project.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if src != "" {
				base, err := baseOnly(src)
				if err != nil {
					return err
				}
				printList(cmd, base, wide)
				return nil
			}
			printAll(cmd, sandbox.Projects(), wide)
			return nil
		},
	}
	cmd.Flags().StringVar(&src, "src", "", "list only this project (default: every project on this host)")
	cmd.Flags().BoolVarP(&wide, "wide", "w", false, "show full sandbox names and project paths (no truncation)")
	return cmd
}

func printList(cmd *cobra.Command, base *sandbox.Base, wide bool) {
	out := cmd.OutOrStdout()
	cur := base.Current()
	states := projectSessionStates(base.Dir)
	fmt.Fprintf(out, listFmt, "", sandbox.IDLen, "ID", "SANDBOX", "STATE")
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
			marker, sandbox.IDLen, base.ID(slug), slugDisp, sessionState(states, slug))
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "* = active (use). enter <s> | exec <s> -- cmd | show [s] | path [s] | rm <s>")
}

// allRow is one rendered host-wide row. The rows are collected before printing
// so the PROJECT column can be sized to what is actually there — and so an empty
// host prints a hint instead of a header with nothing under it.
type allRow struct {
	marker, id, project, slug, state string
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
			})
		}
	}
	if len(rows) == 0 {
		fmt.Fprintln(out, "no sandboxes on this host (create one: sandboxer create <slug>)")
		return
	}
	width := projectWidth(longest, outWidth(out), wide)
	fmt.Fprintf(out, listAllFmt, "", sandbox.IDLen, "ID", width, "PROJECT", "SANDBOX", "STATE")
	for _, r := range rows {
		fmt.Fprintf(out, listAllFmt, r.marker, sandbox.IDLen, r.id,
			width, truncateLeft(r.project, width), r.slug, r.state)
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

// hostSessionStates maps baseDir → slug → container status for every project on
// the host, from one sweep per installed engine (a profile's `backend:` may have
// put sessions on podman AND docker, or on a microVM runner). Best-effort like
// projectSessionStates: an engine problem drops that engine's answers only.
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

// mergeStates folds one engine's slug→status answers into dst, where a
// "running" verdict wins over another engine's leftover record.
func mergeStates(dst, src map[string]string) {
	for slug, s := range src {
		if cur, ok := dst[slug]; !ok || cur != "running" {
			dst[slug] = s
		}
	}
}

// projectSessionStates returns slug→container status for the project's
// persistent sessions, probing every installed engine (a profile's `backend:`
// may have put sessions on either). Best-effort: any engine problem only
// drops that engine's answers, so the listing shows dashes instead of failing
// on an engine-less host.
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

// sessionState folds an engine container status into the STATE column:
// "running" stays, any other recorded status reads "stopped", and a sandbox
// without a session container shows "-".
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
			fmt.Fprintln(out, configLine(rtShow, t.slug, t.profile, backendLabel(rtShow)))
			fmt.Fprintf(out, "== profile (%s) ==\n", t.slug)
			if !dumpFile(out, t.base.ProfileJSONPath(t.slug)) {
				fmt.Fprintln(out, "(no profile)")
			}
			printSourcesBlock(out, t)
			printSessionBlock(out, t, rtShow)
			return nil
		},
	}
	bindExisting(cmd, &f)
	return cmd
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

// printSessionBlock renders show's "== session ==" lines: the deterministic
// session container name, its current state, and whether the session is still
// fresh — the config hash recorded at create time matches the profile AND the
// container still runs the engine's current image — or the next persistent
// enter would recreate the container (stale, naming which of the two went).
// Best-effort: with no engine the state is unknown, never an error.
func printSessionBlock(out io.Writer, t *target, rt config.Runtime) {
	fmt.Fprintln(out, "== session ==")
	name := backend.SessionName(t.slug, t.base.Dir)
	fmt.Fprintf(out, "container: %s\n", name)
	engine, err := backend.ResolveEngine(rt.Backend, config.LoadDefaults())
	if err != nil {
		fmt.Fprintln(out, "state: unknown (no container engine)")
		return
	}
	info := backendInspectSession(engine, name)
	if !info.Exists {
		fmt.Fprintln(out, "state: none (a persistent enter creates it)")
		return
	}
	state := "stopped"
	if info.Running {
		state = "running"
	}
	switch o, ok := sessionHashOpts(t, rt, engine); {
	case !ok:
		fmt.Fprintf(out, "state: %s\n", state)
	case info.Hash != backendWantHash(o):
		fmt.Fprintf(out, "state: %s (stale — the profile changed; re-enter recreates it)\n", state)
	case !backend.ImageFresh(info.ImageID, backendImageID(engine, o.Image)):
		fmt.Fprintf(out, "state: %s (stale — the image was rebuilt; re-enter recreates it)\n", state)
	default:
		fmt.Fprintf(out, "state: %s (fresh)\n", state)
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
	image, spec, err := resolveImage(t.profile, "", io.Discard)
	if err != nil {
		return backend.RunOpts{}, false
	}
	mountDest, srcMounts, mountGen, mountIDs, err := t.mounts()
	if err != nil {
		return backend.RunOpts{}, false
	}
	return backend.RunOpts{
		Engine: engine, Image: image, Spec: spec,
		Dest: t.base.SandboxDir(t.slug), Slug: t.slug, BaseDir: t.base.Dir,
		MountDest: mountDest,
		MountGen:  mountGen,
		MountIDs:  mountIDs,
		SrcMounts: srcMounts,
		HomeDir:   t.base.HomeDir(t.slug),
		DestGen:   t.base.Gen(t.slug),
		AuthEnv:   hostAuthEnv(t.profile),
		RT:        rt, Profile: t.profile,
		ProfileJSONPath: t.base.ProfileJSONPath(t.slug),
		// Paths only, no generation: the argv (and so the session hash this
		// feeds) must match what enter built, which mounts them iff they exist.
		NestedIDFiles: backend.NestedIDFiles(t.base.NestedIDFiles(t.slug)),
		Mem:           rt.Mem, CPU: rt.CPU, Pids: rt.Pids,
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
