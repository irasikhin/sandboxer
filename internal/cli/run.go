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
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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
	fl.StringVar(&doms, "allow-domains", "", "egress allowlist (csv)")
	fl.IntVar(&maxp, "max-parallel", d.MaxParallel, "max concurrent agents")
	fl.IntVar(&nice, "nice", d.Nice, "nice level for agents")
	fl.StringVar(&mem, "mem", d.Mem, "memory limit (systemd MemoryMax, e.g. 2G)")
	fl.StringVar(&cpu, "cpu", d.CPU, "CPU quota (systemd CPUQuota, e.g. 100%)")
	fl.StringVar(&wall, "wall", d.Wall, "wall-clock timeout in seconds")
	fl.BoolVar(&keep, "keep", false, "keep existing .sandboxer state")
	fl.BoolVar(&dry, "dry-run", false, "do not run agents; just create sandboxes")
	return cmd
}
