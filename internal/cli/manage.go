package cli

import (
	"fmt"
	"io"
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
	// (clean, list, doctor) — sessions may live on podman AND docker, AND on the
	// microVM backend (smolvm) whose machines + host-side records would otherwise
	// leak, invisible to clean/doctor.
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
		Use:   "rm [slug]",
		Short: "Remove a sandbox and its state",
		Long: `Remove a sandbox: its managed source worktrees, private agent home, logs,
metadata and the persistent session container. The source branches are
KEPT — the work stays reviewable in each repo; delete them with plain git
when done. Adopted worktrees are never touched.`,
		Example: `  # remove the sandbox "feat" (its configured branches survive)
  sandboxer rm feat

  # remove the active sandbox
  sandboxer rm`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := resolveTarget(f, posArg(args))
			if err != nil {
				return err
			}
			// The session container goes first, while the engine labels still
			// match an existing base dir; best-effort — an engine-less host
			// must still get its files removed.
			removeSessionBestEffort(t, f, cmd.ErrOrStderr())
			if err := t.base.Remove(t.slug); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed sandbox: %s\n", t.slug)
			return nil
		},
	}
	bindExisting(cmd, &f)
	return cmd
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
