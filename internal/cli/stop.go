package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/backend"
	"github.com/irasikhin/sandboxer/internal/config"
)

func init() { register(newStopCmd) }

// backendStopSession is the stop seam beside backendRun (lifecycle.go),
// overridable in tests so the command is exercised without a real engine.
var backendStopSession = backend.StopSession

func newStopCmd() *cobra.Command {
	var f commonFlags
	cmd := &cobra.Command{
		Use:   "stop [slug|id ...]",
		Short: "Stop one or more sandboxes' persistent sessions (kept for a later enter)",
		Long: `Stop a sandbox's persistent session container and its egress proxy. The
container, its networks and the sandbox files all stay in place, so the next
'sandboxer enter' resumes the session with a plain start. Use 'sandboxer rm'
to remove the sandbox (and its session) entirely.

Each argument is a slug in the current project, or an ID from 'sandboxer list'
(any unambiguous prefix) — which stops that sandbox in ITS project, no cd and
no --src needed. Several may be given; every one of them is resolved before
anything is stopped, so a typo stops nothing. With no argument, the active
sandbox is stopped.`,
		Example: `  # stop the sandbox "feat"
  sandboxer stop feat

  # stop the active sandbox
  sandboxer stop

  # stop two sandboxes seen in 'sandboxer list', in whatever projects they live
  sandboxer stop 9f0e1122 3c4d`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				args = []string{""} // no argument: the active sandbox, as before
			}
			// Resolve EVERY argument first, exactly like rm: a batch that
			// silently skips a mistyped id — or stops a list of which one
			// argument was wrong — is the confusion a batch must not produce.
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
				rt, err := t.runtime(f)
				if err != nil {
					return err
				}
				if err := config.ValidateBackend(rt); err != nil {
					return err
				}
				// Stop the session where it actually IS. The profile's backend only
				// says where the NEXT session would be created, so resolving from it
				// alone made `stop` report success while a session created under a
				// since-edited `backend =` kept running (same drift rm suffered from —
				// see backend.RemoveSessionAnywhere). Falling back to the resolved
				// engine keeps the diagnostics for a genuinely absent session.
				engine := backendSessionEngine(t.slug, t.base.Dir, config.LoadDefaults())
				if engine == "" {
					if engine, err = backend.ResolveEngine(rt.Backend, config.LoadDefaults()); err != nil {
						return err
					}
				}
				// Save the live tmux layout before the stop kills the container's
				// processes, so the next enter's start restores it — a stop parks the
				// session, it never discards it (only rm does).
				backend.SaveSessionState(engine, backend.SessionName(t.slug, t.base.Dir), t.base.SessionStatePath(t.slug))
				// StopSession is idempotent: a missing or already-stopped session
				// succeeds, so stop is always safe to run.
				if err := backendStopSession(engine, t.slug, t.base.Dir); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "stopped session: %s (resume: sandboxer enter %s)%s\n",
					backend.SessionName(t.slug, t.base.Dir), t.slug, otherProject(t))
			}
			return nil
		},
	}
	bindExisting(cmd, &f)
	return cmd
}
