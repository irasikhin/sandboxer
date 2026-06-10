package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/runner"
	"github.com/irasikhin/sandboxer/internal/sandbox"
)

func init() { register(newRunCmd) }

func newRunCmd() *cobra.Command {
	var (
		src, configPath             string
		model, agent, backend, doms string
		mem, cpu, wall              string
		maxp                        int
		keep, dry, noSetup          bool
	)
	d := config.LoadDefaults()
	cmd := &cobra.Command{
		Use:   "run [tasks-file]",
		Short: "Run a batch of autonomous agents, one sandbox per task",
		Example: `  # one sandbox per [slug] section, 4 agents at a time
  sandboxer run tasks.txt --agent claude --max-parallel 4

  # no file given → ./sandboxer.tasks (see sandboxer.tasks.example)
  sandboxer run

  # cap each agent and time it out (both backends)
  sandboxer run tasks.txt --mem 2G --cpu 100% --wall 1800`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Resolve -f/--config the same way the lifecycle commands do: a file, a
			// directory of profiles, a named profile from the store, or (when unset)
			// an auto-discovered ./.sandboxer.yaml. The single resolved profile
			// applies to every task in the batch.
			file, _, err := resolveProfileFile(configPath, "")
			if err != nil {
				return err
			}
			configPath = file
			res, err := runner.Run(runner.Options{
				Src:        src,
				ConfigPath: configPath,
				TasksFile:  posArg(args),
				Overrides:  config.Overrides{Model: model, Agent: agent, Backend: backend, Domains: doms},
				Defaults:   d,
				Image:      d.Image,
				MaxP:       maxp,
				Mem:        mem,
				CPU:        cpu,
				Wall:       wall,
				Keep:       keep,
				DryRun:     dry,
				NoSetup:    noSetup,
				Stdout:     cmd.OutOrStdout(),
				Stderr:     cmd.ErrOrStderr(),
			})
			if err != nil {
				return err
			}
			base, err := sandbox.ResolveBase(res.Root)
			if err != nil {
				return err
			}
			printList(cmd, base, false)
			if res.Failed > 0 {
				// The per-agent diagnostics and the summary line are already on
				// stderr/stdout; just carry the non-zero exit so scripts and CI
				// see the partial failure instead of a green run.
				return silentErr{fmt.Errorf("%d task(s) failed", res.Failed)}
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&src, "src", "", "project root")
	fl.StringVarP(&configPath, "config", "f", "", "profile: a file, a directory of profiles, or a named profile (store: ~/.config/sandboxer/profiles)")
	fl.StringVar(&model, "model", "", "model override")
	fl.StringVar(&agent, "agent", "", "agent override")
	fl.StringVar(&backend, "backend", "", "backend: podman | docker")
	fl.StringVar(&doms, "allow-domains", "", "egress allowlist, csv (e.g. api.anthropic.com,github.com)")
	fl.IntVar(&maxp, "max-parallel", d.MaxParallel, "max concurrent agents")
	fl.StringVar(&mem, "mem", d.Mem, "per-agent memory cap, e.g. 2G (--memory)")
	fl.StringVar(&cpu, "cpu", d.CPU, "per-agent CPU cap, e.g. 100% or 1.5 (--cpus)")
	fl.StringVar(&wall, "wall", d.Wall, "per-agent wall-clock timeout in seconds")
	fl.BoolVar(&keep, "keep", false, "keep existing .sandboxer state")
	fl.BoolVar(&dry, "dry-run", false, "do not run agents; just create sandboxes")
	fl.BoolVar(&noSetup, "no-setup", false, "skip each profile's one-time setup script")
	return cmd
}
