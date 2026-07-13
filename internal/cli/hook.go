package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/sandbox"
)

func init() { register(newHookCmd) }

// newHookCmd builds the `hook` parent and its shell-integration subcommands.
// `hook` is read-only and allowed everywhere (including inside the container):
// it only reads already-persisted state and prints, never building or starting
// anything.
func newHookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hook",
		Short: "Shell integration: surface the active sandbox to the host shell",
		Long: `Emit the active sandbox as host-shell exports so an editor/terminal/direnv
can see which sandbox is selected for the current project.

This is purely read-only: it prints the already-persisted state and never
creates, starts or builds anything — a 'cd' into a project is free. It is a
no-op (exit 0, nothing emitted) outside a sandboxer project or when no sandbox
is active, so an .envrc can call it unconditionally.`,
	}
	cmd.AddCommand(newHookDirenvCmd())
	return cmd
}

func newHookDirenvCmd() *cobra.Command {
	var src string
	cmd := &cobra.Command{
		Use:   "direnv",
		Short: "Print the active sandbox as host-shell exports for an .envrc",
		Long: `Print 'export' lines describing the active sandbox, for evaluation by a host
shell (typically via direnv):

  eval "$(sandboxer hook direnv)"

Exported (only when an active sandbox exists):

  SANDBOXER_SLUG           the active sandbox slug
  SANDBOXER_SRC            the project root (absolute)
  SANDBOXER_BACKEND        the configured backend     (if recorded)
  SANDBOXER_ALLOW_DOMAINS  the egress allowlist (csv) (if recorded)

Outside a sandboxer project, or with no active sandbox, nothing is exported
(exit 0) so the calling .envrc never errors.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return emitDirenv(cmd.OutOrStdout(), firstNonEmpty(src, getwd()))
		},
	}
	cmd.Flags().StringVar(&src, "src", "", "project root (default: cwd)")
	return cmd
}

// emitDirenv writes the active sandbox's host-shell exports to w. It is a
// no-op (no output, nil error) when root is not a sandboxer project or has no
// active sandbox. All work is a pure read of persisted state.
func emitDirenv(w io.Writer, root string) error {
	base, err := sandbox.OpenBase(root)
	if err != nil {
		return err
	}
	if base == nil {
		// Not a sandboxer project: stay silent so an unconditional .envrc is safe.
		return nil
	}
	slug := base.Current()
	if slug == "" {
		fmt.Fprintln(w, "# sandboxer: no active sandbox")
		return nil
	}

	// Pairs are emitted in a stable order so the output is deterministic.
	vars := map[string]string{
		"SANDBOXER_SLUG": slug,
		"SANDBOXER_SRC":  base.Src,
	}
	// The persisted, already-resolved values — no Runtime is re-resolved (that
	// would be heavy work and, worse, could touch the engine). Backend comes from
	// the sandbox's stored profile snapshot; the egress allowlist is the base's
	// resolved run.env value.
	if prof := loadStoredProfile(base, slug); prof != nil && prof.Backend != "" {
		vars["SANDBOXER_BACKEND"] = prof.Backend
	}
	if base.Domains != "" {
		vars["SANDBOXER_ALLOW_DOMAINS"] = base.Domains
	}

	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "export %s=%s\n", k, shellQuote(vars[k]))
	}
	return nil
}

// shellQuote single-quotes s for safe `eval` in a POSIX shell: the value is
// wrapped in single quotes and any embedded single quote is rendered as the
// '\” escape sequence. This neutralizes spaces, $, backticks and quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
