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
			engine, err := backend.ResolveEngine(rt.Backend, config.LoadDefaults())
			if err != nil {
				return err
			}
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
