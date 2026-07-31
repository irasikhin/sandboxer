package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/backend"
	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/registry"
	"github.com/irasikhin/sandboxer/internal/sandbox"
)

// Session-teardown seams beside backendRun (lifecycle.go), so rm/rm-all are
// exercised in tests without a real engine.
var (
	backendRemoveSession     = backend.RemoveSession
	backendRemoveAllSessions = backend.RemoveAllSessions
	// backendSweepEngines enumerates every engine a sweep or report must visit
	// (clean, list, doctor) — sessions may live on podman AND docker, AND on
	// either microVM runner (smolvm, microsandbox) whose machines + host-side
	// records would otherwise leak, invisible to clean/doctor.
	backendSweepEngines = backend.SweepEngines
)

func init() {
	register(newRmCmd)
	register(newUseCmd)
	register(newAgentsCmd)
}

func newRmCmd() *cobra.Command {
	var f commonFlags
	cmd := &cobra.Command{
		Use:   "rm [slug|id ...]",
		Short: "Remove one or more sandboxes and their state",
		Long: `Remove a sandbox: its managed source worktrees, private agent home, logs,
metadata and the persistent session container. The source branches are
KEPT — the work stays reviewable in each repo; delete them with plain git
when done. Adopted worktrees are never touched.

Each argument is a slug in the current project, or an ID from 'sandboxer list'
(any unambiguous prefix) — which removes that sandbox in ITS project, no cd
and no --src needed, including one whose project directory is gone. Several
may be given; every one of them is resolved before anything is removed, so a
typo removes nothing.`,
		Example: `  # remove the sandbox "feat" (its configured branches survive)
  sandboxer rm feat

  # remove two sandboxes seen in 'sandboxer list', in whatever projects they live
  sandboxer rm 9f0e1122 3c4d

  # remove the active sandbox
  sandboxer rm`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				args = []string{""} // no argument: the active sandbox, as before
			}
			// Resolve EVERY argument first: a half-done removal is the one
			// outcome a batch must not produce, and an unknown id is exactly
			// the mistake a batch invites.
			targets := make([]*target, 0, len(args))
			for _, arg := range args {
				t, err := resolveTarget(f, arg)
				if err != nil {
					return err
				}
				if !known(t) {
					return fmt.Errorf("no such sandbox: %s — 'sandboxer list' shows every sandbox on this host, "+
						"by slug (this project) and by id (any project)", firstNonEmpty(arg, t.slug))
				}
				targets = append(targets, t)
			}
			for _, t := range targets {
				// The session container goes first, while the engine labels still
				// match an existing base dir; best-effort — an engine-less host
				// must still get its files removed.
				removeSessionBestEffort(t, f, cmd.ErrOrStderr())
				if err := t.base.Remove(t.slug); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "removed sandbox: %s%s\n", t.slug, otherProject(t))
			}
			return nil
		},
	}
	bindExisting(cmd, &f)
	return cmd
}

// known reports whether the target names a sandbox that actually exists. A
// removal is the one place a typo must not pass silently: resolveTarget happily
// shapes any token into a target, so without this `rm bogus` would report
// "removed sandbox: bogus" having removed nothing — and in a batch it would
// hide which of the arguments was wrong. The registration is the record, and
// any leftover artifact counts too: a create that died part-way never got to
// register, and that half-written state is exactly what rm is for.
func known(t *target) bool {
	if slices.Contains(t.base.Agents(), t.slug) {
		return true
	}
	for _, p := range []string{
		t.base.SandboxDir(t.slug),
		t.base.ProfileJSONPath(t.slug),
		t.base.HomeDir(t.slug),
		t.base.MetaFilePath(t.slug),
	} {
		if fileExists(p) {
			return true
		}
	}
	return false
}

// otherProject names the target's project when it is NOT the one we are
// standing in — a sandbox reached by id lives elsewhere, and "removed sandbox:
// feat" with no hint of WHERE reads like the local one just went.
func otherProject(t *target) string {
	if wd, err := filepath.Abs(getwd()); err == nil && wd == t.base.Src {
		return ""
	}
	return " (" + t.base.Src + ")"
}

// removeSessionBestEffort tears down the sandbox's persistent session
// container (and its egress resources) before the files go. Best-effort BY
// DESIGN: file removal must succeed on an engine-less host, so any engine
// problem only warns — at worst a container is left behind, which doctor
// reports as an orphan once the base dir is gone.
func removeSessionBestEffort(t *target, f commonFlags, errOut io.Writer) {
	rt, err := t.runtime(f)
	if err != nil {
		fmt.Fprintf(errOut, "sandboxer: session cleanup skipped: %v\n", err)
		return
	}
	engine, err := backend.ResolveEngine(rt.Backend, config.LoadDefaults())
	if err != nil {
		fmt.Fprintf(errOut, "sandboxer: session cleanup skipped: %v\n", err)
		return
	}
	if err := backendRemoveSession(engine, t.slug, t.base.Dir); err != nil {
		fmt.Fprintf(errOut, "sandboxer: session cleanup failed: %v\n", err)
	}
}

// saveSessionLayout best-effort captures the sandbox's live tmux layout to the
// host (backend.SaveSessionState) so a teardown that KEEPS the sandbox —
// recreate, stop — can restore it on the next attach. Silent on any engine
// problem: a missing layout must never block the operation. NOT called by
// rm/clean, which discard the sandbox and its saved layout on purpose.
func saveSessionLayout(t *target, f commonFlags) {
	rt, err := t.runtime(f)
	if err != nil {
		return
	}
	engine, err := backend.ResolveEngine(rt.Backend, config.LoadDefaults())
	if err != nil {
		return
	}
	backend.SaveSessionState(engine, backend.SessionName(t.slug, t.base.Dir), t.base.SessionStatePath(t.slug))
}

func newUseCmd() *cobra.Command {
	var src string
	var clear bool
	cmd := &cobra.Command{
		Use:   "use [slug]",
		Short: "Get, set or clear the active sandbox",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := sandbox.ResolveBase(firstNonEmpty(src, getwd()))
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if clear {
				if err := base.ClearCurrent(); err != nil {
					return err
				}
				fmt.Fprintln(out, "active sandbox cleared")
				return nil
			}
			slug := posArg(args)
			if slug == "" {
				if cur := base.Current(); cur != "" {
					fmt.Fprintln(out, cur)
				} else {
					fmt.Fprintln(out, "(no active sandbox)")
				}
				return nil
			}
			slug = config.Sanitize(slug)
			if err := config.ValidSlug(slug); err != nil {
				return err
			}
			if err := base.SetCurrent(slug); err != nil {
				return err
			}
			fmt.Fprintf(out, "active sandbox: %s\n", slug)
			return nil
		},
	}
	cmd.Flags().StringVar(&src, "src", "", "project root")
	cmd.Flags().BoolVar(&clear, "clear", false, "clear the active sandbox")
	return cmd
}

// orDash renders an empty cell as "-" so the table reads cleanly.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func newAgentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "List the coding agents baked into the toolbox image",
		Long: `List the coding agents baked into the toolbox image. A sandbox is not bound
to one agent: run any of them with 'sandboxer exec <slug> -- <agent>'. For
each: its binary, whether it ships in the image, and the env vars it reads for
auth — nothing is passed through from the host; log in or export those vars
INSIDE the sandbox (its private $HOME persists).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "AGENT\tBIN\tIMAGE\tRESUME\tENV")
			for _, name := range registry.Names() {
				a, _ := registry.Get(name)
				image := "yes"
				if a.Image != nil && !*a.Image {
					image = "no"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
					name, a.Bin, image, orDash(strings.Join(a.Resume, " ")),
					orDash(strings.Join(a.AuthEnv, ",")))
			}
			return tw.Flush()
		},
	}
	return cmd
}
