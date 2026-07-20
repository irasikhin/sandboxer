package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/backend"
	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/sandbox"
)

// sessionStates is the session-enumeration seam for list and doctor,
// overridable in tests so the STATE column renders without a real engine.
var sessionStates = backend.SessionStates

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

func newListCmd() *cobra.Command {
	var src string
	var wide bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"status"},
		Short:   "List sandboxes and their status",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			base, err := baseOnly(src)
			if err != nil {
				return err
			}
			printList(cmd, base, wide)
			return nil
		},
	}
	cmd.Flags().StringVar(&src, "src", "", "project root")
	cmd.Flags().BoolVarP(&wide, "wide", "w", false, "show full sandbox names (no truncation)")
	return cmd
}

func printList(cmd *cobra.Command, base *sandbox.Base, wide bool) {
	out := cmd.OutOrStdout()
	cur := base.Current()
	states := projectSessionStates(base.Dir)
	fmt.Fprintf(out, "%-2s %-16s %-8s %-9s %-5s %s\n", "", "SANDBOX", "STATE", "EXIT", "SEC", "RESULT")
	for _, slug := range base.Agents() {
		exit, secs := readMeta(base.MetaFilePath(slug))
		res := jsonResult(base.LogPath(slug, "json"))
		marker := ""
		if slug == cur {
			marker = "*"
		}
		slugDisp, resDisp := slug, res
		if !wide {
			slugDisp = truncate(slug, 16)
			resDisp = truncate(res, 50)
		}
		fmt.Fprintf(out, "%-2s %-16s %-8s %-9s %-5s %s\n",
			marker, slugDisp, sessionState(states, slug), exit, secs, resDisp)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "* = active (use). enter <s> | exec <s> -- cmd | show [s] | path [s] | rm <s>")
}

// projectSessionStates returns slug→container status for the project's
// persistent sessions, probing every installed engine (a profile's `backend:`
// may have put sessions on either). Best-effort: any engine problem only
// drops that engine's answers, so the listing shows dashes instead of failing
// on an engine-less host.
func projectSessionStates(baseDir string) map[string]string {
	var states map[string]string
	for _, engine := range backendInstalledEngines(config.LoadDefaults()) {
		st, err := sessionStates(engine, baseDir)
		if err != nil {
			continue
		}
		if states == nil {
			states = make(map[string]string, len(st))
		}
		for slug, s := range st {
			// A "running" verdict wins over another engine's leftover record.
			if cur, ok := states[slug]; !ok || cur != "running" {
				states[slug] = s
			}
		}
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
		Mem:             rt.Mem, CPU: rt.CPU, Pids: rt.Pids,
		NoEgress: noEgress(),
	}, true
}

func readMeta(path string) (exit, secs string) {
	exit, secs = "-", "-"
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "exit="); ok {
			exit = v
		} else if v, ok := strings.CutPrefix(line, "secs="); ok {
			secs = v
		}
	}
	return
}

// jsonResult extracts the agent's result/error string from its output log.
func jsonResult(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		return ""
	}
	for _, k := range []string{"result", "error"} {
		if v, ok := m[k]; ok {
			return strings.Join(strings.Fields(fmt.Sprint(v)), " ")
		}
	}
	return ""
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
