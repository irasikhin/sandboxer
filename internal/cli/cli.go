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
		// sandboxer is a HOST tool. The binary is not baked into the toolbox image
		// (egress is a separate squid sidecar), so it is normally absent inside the
		// sandbox; this guard is belt-and-suspenders for a custom image that bakes
		// it in anyway — deny-all when SANDBOXER_IN_CONTAINER is set. The agent
		// works on the sandbox worktree; lifecycle and cleanup (create/rm/clean)
		// run from the host.
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if inContainer() {
				return fmt.Errorf("sandboxer is a host tool and is not available inside the sandbox — run %q on the host", cmd.Name())
			}
			return nil
		},
	}
	// Help is organized into three activity groups (image / data / sandbox)
	// plus a catch-all — flat verbs, grouped only for readability.
	root.AddGroup(
		&cobra.Group{ID: groupSetup, Title: "Image & config:"},
		&cobra.Group{ID: groupSandbox, Title: "Sandbox (enter & work):"},
		&cobra.Group{ID: groupData, Title: "Data (clean / show):"},
		&cobra.Group{ID: groupOther, Title: "Other:"},
	)
	// Subcommands are registered by their respective files via register().
	for _, add := range commandFactories {
		c := add()
		if g, ok := commandGroups[c.Name()]; ok {
			c.GroupID = g
		}
		root.AddCommand(c)
	}
	return root
}

// Command groups for --help. Flat verbs, grouped for readability only — there is
// no nesting except the genuine noun groups (image, profile).
const (
	groupSetup   = "setup"
	groupSandbox = "sandbox"
	groupData    = "data"
	groupOther   = "other"
)

// commandGroups maps a top-level command name to its help group. Names absent
// here (completion, hook) fall under cobra's "Additional Commands".
var commandGroups = map[string]string{
	"image":   groupSetup,
	"config":  groupSetup,
	"profile": groupSetup,

	"create":   groupSandbox,
	"enter":    groupSandbox,
	"exec":     groupSandbox,
	"stop":     groupSandbox,
	"recreate": groupSandbox,
	"rm":       groupSandbox,
	"list":     groupSandbox,
	"use":      groupSandbox,

	"clean": groupData,
	"show":  groupData,
	"path":  groupData,

	"compose": groupOther,
	"agents":  groupOther,
	"doctor":  groupOther,
}

// commandFactories is populated by each command file's init() so the command
// set is assembled without a central edit point.
var commandFactories []func() *cobra.Command

func register(factory func() *cobra.Command) {
	commandFactories = append(commandFactories, factory)
}

const rootLong = `sandboxer — config-driven isolated sandboxes for coding agents.

A sandbox exposes SOURCES: git repos checked out into per-sandbox worktrees
under the project's ./sandboxes/<slug>/<repo>/<branch> (auto-git-ignored;
relocatable via the profile's worktreesDir) — every source names its branch
explicitly, and its directory is named after it. The container
sees ONLY the files the srcs select — git metadata never enters it — so the
agent edits files while your working tree, branches and git stay untouched;
you review and commit the result with plain git on the host. srcs entries can
narrow a repo with gitignore-style include patterns, span several repos, or
pin an existing branch/worktree; other trees come in via extraMounts.

The agent runs inside a podman/docker container built from the toolbox image
(the agents baked in — see 'sandboxer agents'); each sandbox has its own
isolated home, and network/proxy are wired per-config. Credentials never come
from the host — log in or export keys inside the sandbox.

Config: flags + SANDBOXER_* env, plus sandboxer.nix for the structured fields
(srcs, extraMounts, env, setup, tools, image — evaluated with a restricted nix
eval; nix on the host is required). One profile per file, or several under a
profiles attrset (pick a section with 'create <name>'); reuse between profiles
is ordinary nix (let/functions).

Tips:
  • 'sandboxer use <slug>' sets an active sandbox so you can omit the slug after.
  • Review and commit with plain git ON THE HOST: git log <branch>
    (each source repo gets its own worktree, on the branch you configured).
  • Outbound traffic is restricted to an egress allowlist
    (egress.allowedDomains / --allow-domains; disable with SANDBOXER_NO_EGRESS=1).
  • Each create/enter/exec prints the resolved backend/egress/profile it used.`
