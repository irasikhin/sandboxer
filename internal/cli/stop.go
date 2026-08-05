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
		Use:   "stop [slug]",
		Short: "Stop a sandbox's persistent session (kept for a later enter)",
		Long: `Stop a sandbox's persistent session container and its egress proxy. The
container, its networks and the sandbox files all stay in place, so the next
'sandboxer enter' resumes the session with a plain start. Use 'sandboxer rm'
to remove the sandbox (and its session) entirely.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := resolveTarget(f, posArg(args))
			if err != nil {
				return err
			}
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
			fmt.Fprintf(cmd.OutOrStdout(), "stopped session: %s (resume: sandboxer enter %s)\n",
				backend.SessionName(t.slug, t.base.Dir), t.slug)
			return nil
		},
	}
	bindExisting(cmd, &f)
	return cmd
}
