package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	register(newDiffCmd)
	register(newShowCmd)
}

// baseOnly resolves the base for commands that don't need a specific slug.
func baseOnly(src string) (*sandbox.Base, error) {
	root := firstNonEmpty(src, getwd())
	if !sandbox.RunEnvExists(root) {
		return nil, fmt.Errorf("no sandboxes in %s/.sandboxer (create one: sandboxer create <slug>)", root)
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
	fmt.Fprintln(out, "* = active (use). enter <s> | exec <s> -- cmd | diff [s] | push [s] | rm <s>")
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

// sandboxDiff shows what changed in a sandbox's pulled deps versus their
// origins (one `diff -ruN` per manifest entry). Empty when there is no manifest.
func sandboxDiff(base *sandbox.Base, slug string) string {
	data, err := os.ReadFile(base.ManifestPath(slug))
	if err != nil {
		return ""
	}
	var entries []struct {
		Origin      string `json:"origin"`
		SandboxPath string `json:"sandboxPath"`
	}
	if json.Unmarshal(data, &entries) != nil {
		return ""
	}
	var b strings.Builder
	for _, e := range entries {
		if e.Origin == "" || e.SandboxPath == "" {
			continue
		}
		// diff exits 1 when files differ; the diff text is still on stdout.
		o, _ := exec.Command("diff", "-ruN", e.Origin, e.SandboxPath).Output()
		b.Write(o)
	}
	return b.String()
}

func newDiffCmd() *cobra.Command {
	var src string
	cmd := &cobra.Command{
		Use:   "diff [slug]",
		Short: "Show what changed in a sandbox's deps versus their origins",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := baseOnly(src)
			if err != nil {
				return err
			}
			if _, err := exec.LookPath("diff"); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "sandboxer: diff(1) not found on PATH — cannot show changes")
			}
			out := cmd.OutOrStdout()
			only := ""
			if p := posArg(args); p != "" {
				only = config.Sanitize(p)
			}
			for _, slug := range base.Agents() {
				if only != "" && slug != only {
					continue
				}
				// Only print a section when there is an actual change.
				if d := sandboxDiff(base, slug); d != "" {
					fmt.Fprintf(out, "===== %s =====\n", slug)
					fmt.Fprint(out, d)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&src, "src", "", "project root")
	return cmd
}

func newShowCmd() *cobra.Command {
	var f commonFlags
	cmd := &cobra.Command{
		Use:   "show [slug]",
		Short: "Show the profile and dependency manifest for a sandbox",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if inContainer() {
				fmt.Fprintln(out, "== profile ==")
				dumpFile(out, "/run/sandboxer/profile.json")
				fmt.Fprintln(out, "== manifest ==")
				dumpFile(out, "/run/sandboxer/manifest.json")
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
			fmt.Fprintln(out, "== manifest ==")
			if !dumpFile(out, t.base.ManifestPath(t.slug)) {
				fmt.Fprintln(out, "(no manifest)")
			}
			printSessionBlock(out, t, rtShow)
			return nil
		},
	}
	bindExisting(cmd, &f)
	return cmd
}

// printSessionBlock renders show's "== session ==" lines: the deterministic
// session container name, its current state, and whether the config hash
// recorded at create time still matches the profile (fresh) or the next
// persistent enter would recreate the container (stale). Best-effort: with no
// engine the state is unknown, never an error.
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
	case info.Hash == backendWantHash(o):
		fmt.Fprintf(out, "state: %s (fresh)\n", state)
	default:
		fmt.Fprintf(out, "state: %s (stale — the profile changed; re-enter recreates it)\n", state)
	}
}

// sessionHashOpts assembles the RunOpts SessionWantHash needs to recompute
// the session's config hash — the same fields enter passes, minus the stdio.
// The profile's MCP-server domains are folded into the allowlist exactly as
// enter does before hashing (applyMCP), or an MCP-enabled profile would
// permanently read as stale here. ok=false when the image or the MCP set
// cannot be resolved, leaving freshness unjudged.
func sessionHashOpts(t *target, rt config.Runtime, engine string) (backend.RunOpts, bool) {
	image, spec, err := resolveImage(t.profile)
	if err != nil {
		return backend.RunOpts{}, false
	}
	domains, err := mcpAllowDomains(t.profile, rt.Domains)
	if err != nil {
		return backend.RunOpts{}, false
	}
	rt.Domains = domains
	return backend.RunOpts{
		Engine: engine, Image: image, Spec: spec,
		Dest: t.base.SandboxDir(t.slug), Slug: t.slug, BaseDir: t.base.Dir,
		HomeDir: t.base.HomeDir(t.slug),
		RT:      rt, Profile: t.profile,
		ProfileJSONPath: t.base.ProfileJSONPath(t.slug), ManifestPath: t.base.ManifestPath(t.slug),
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
