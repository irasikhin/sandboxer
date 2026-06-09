// Package cli wires the cobra command tree for the sandboxer CLI.
package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// Version is the binary version, injected from main (set via -ldflags at
// release time, "dev" otherwise).
var Version = "dev"

// Run builds the command tree, executes it against the given args and IO
// streams, and returns the process exit code. It never panics on a normal
// command error — those are reported and turned into exit code 1.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	root := newRootCmd()
	root.SetArgs(args)
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	if err := root.Execute(); err != nil {
		// Cobra already prints usage on arg errors; command bodies print their
		// own diagnostics via cmd.PrintErrln. Just surface a final marker.
		if _, ok := err.(silentErr); !ok {
			fmt.Fprintln(stderr, "sandboxer:", err)
		}
		return 1
	}
	return 0
}

// silentErr lets a command signal failure (exit 1) without Run printing it
// again — used when the command has already emitted a precise message.
type silentErr struct{ err error }

func (e silentErr) Error() string { return e.err.Error() }

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "sandboxer",
		Short:         "Config-driven, multi-agent, containerized dev sandboxes",
		Long:          rootLong,
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		// Inside the container only inspection / dependency sync is allowed.
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if !inContainer() {
				return nil
			}
			switch cmd.Name() {
			case "create", "enter", "exec", "run", "rm", "rm-all", "use":
				return fmt.Errorf("command %q is not available inside the container (only pull/push/show/list/diff)", cmd.Name())
			}
			return nil
		},
	}
	// Subcommands are registered by their respective files via register().
	for _, add := range commandFactories {
		root.AddCommand(add())
	}
	return root
}

// commandFactories is populated by each command file's init() so the command
// set is assembled without a central edit point.
var commandFactories []func() *cobra.Command

func register(factory func() *cobra.Command) {
	commandFactories = append(commandFactories, factory)
}

const rootLong = `sandboxer — config-driven isolated sandboxes for coding agents.

A sandbox is a directory under .sandboxer/<slug>/ holding only the deps you list
(each located by path suffix under your roots and copied in); nothing is copied by
default and no git is involved. The agent runs inside a podman/docker container
built from the toolbox image (the agents baked in: claude/codex/opencode/crush/…);
each sandbox has its own isolated home, and network, proxy and credentials are
wired per-config.

Config: flags + SANDBOXER_* env, with an optional .sandboxer.yaml profile for
structured fields (roots/deps, extraMounts, env). A profile file can hold one
profile or several under a profiles: map (pick a section with 'create <name>').

Tips:
  • 'sandboxer use <slug>' sets an active sandbox so you can omit the slug after.
  • Outbound traffic is restricted to an egress allowlist
    (network.allowedDomains / --allow-domains; disable with SANDBOXER_NO_EGRESS=1).
  • Each create/enter/exec prints the resolved agent/backend/model/egress it used.`
