package cli

import (
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
		maxp, nice                  int
		keep, dry                   bool
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
			// Match the lifecycle commands: when no --config is given, pick up a
			// sandboxer.yaml from the cwd so `run` and `create` resolve identically.
			if configPath == "" {
				configPath, _ = resolveProfileFile("", "")
			}
			res, err := runner.Run(runner.Options{
				Src:        src,
				ConfigPath: configPath,
				TasksFile:  posArg(args),
				Overrides:  config.Overrides{Model: model, Agent: agent, Backend: backend, Domains: doms},
				Defaults:   d,
				Image:      d.Image,
				MaxP:       maxp,
				Nice:       nice,
				Mem:        mem,
				CPU:        cpu,
				Wall:       wall,
				Keep:       keep,
				DryRun:     dry,
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
			printList(cmd, base)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&src, "src", "", "project root")
	fl.StringVar(&configPath, "config", "", "profile file (sandboxer.yaml)")
	fl.StringVar(&model, "model", "", "model override")
	fl.StringVar(&agent, "agent", "", "agent override")
	fl.StringVar(&backend, "backend", "", "backend: native | podman | docker")
	fl.StringVar(&doms, "allow-domains", "", "egress allowlist, csv (e.g. api.anthropic.com,github.com)")
	fl.IntVar(&maxp, "max-parallel", d.MaxParallel, "max concurrent agents")
	fl.IntVar(&nice, "nice", d.Nice, "nice level for agents")
	fl.StringVar(&mem, "mem", d.Mem, "per-agent memory cap, e.g. 2G (native: MemoryMax; container: --memory)")
	fl.StringVar(&cpu, "cpu", d.CPU, "per-agent CPU cap, e.g. 100% or 1.5 (native: CPUQuota; container: --cpus)")
	fl.StringVar(&wall, "wall", d.Wall, "per-agent wall-clock timeout in seconds (both backends)")
	fl.BoolVar(&keep, "keep", false, "keep existing .sandboxer state")
	fl.BoolVar(&dry, "dry-run", false, "do not run agents; just create sandboxes")
	return cmd
}
