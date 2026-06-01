package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/sandbox"
)

func init() {
	register(newListCmd)
	register(newDiffCmd)
	register(newShowCmd)
}

// baseOnly resolves the base for commands that don't need a specific slug.
func baseOnly(src string) (*sandbox.Base, error) {
	root := firstNonEmpty(src, getwd())
	if !sandbox.RunEnvExists(root) {
		return nil, fmt.Errorf("no sandboxes in %s/.sandboxer (create one: sandboxer create <slug>)", root)
	}
	return sandbox.ResolveBase(root)
}

func newListCmd() *cobra.Command {
	var src string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"status"},
		Short:   "List sandboxes and their status",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			base, err := baseOnly(src)
			if err != nil {
				return err
			}
			printList(cmd, base)
			return nil
		},
	}
	cmd.Flags().StringVar(&src, "src", "", "project root")
	return cmd
}

func printList(cmd *cobra.Command, base *sandbox.Base) {
	out := cmd.OutOrStdout()
	cur := base.Current()
	fmt.Fprintf(out, "%-2s %-16s %-9s %-5s %-8s %s\n", "", "SANDBOX", "EXIT", "SEC", "CHANGED", "RESULT")
	for _, slug := range base.Agents() {
		exit, secs := readMeta(base.MetaFilePath(slug))
		changed := base.ChangedFiles(slug)
		res := jsonResult(base.LogPath(slug, "json"))
		marker := ""
		if slug == cur {
			marker = "*"
		}
		fmt.Fprintf(out, "%-2s %-16s %-9s %-5s %-8d %s\n",
			marker, truncate(slug, 16), exit, secs, changed, truncate(res, 50))
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "* = active (use). enter <s> | exec <s> -- cmd | diff [s] | return [s...] | rm <s>")
}

func newDiffCmd() *cobra.Command {
	var src string
	cmd := &cobra.Command{
		Use:   "diff [slug]",
		Short: "Show the diff of one or all sandboxes against their snapshot base",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := baseOnly(src)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			only := ""
			if p := posArg(args); p != "" {
				only = config.Sanitize(p)
			}
			for _, slug := range base.Agents() {
				if only != "" && slug != only {
					continue
				}
				fmt.Fprintf(out, "===== %s =====\n", slug)
				d, _ := base.Diff(slug)
				if d != "" {
					fmt.Fprint(out, d)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&src, "src", "", "project root")
	return cmd
}

func newShowCmd() *cobra.Command {
	var f commonFlags
	cmd := &cobra.Command{
		Use:   "show [slug]",
		Short: "Show the profile and dependency manifest for a sandbox",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if inContainer() {
				fmt.Fprintln(out, "== profile ==")
				dumpFile(out, "/run/sandboxer/profile.json")
				fmt.Fprintln(out, "== manifest ==")
				dumpFile(out, "/run/sandboxer/manifest.json")
				return nil
			}
			t, err := resolveTarget(f, posArg(args))
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "== profile (%s) ==\n", t.slug)
			if !dumpFile(out, t.base.ProfileJSONPath(t.slug)) {
				fmt.Fprintln(out, "(no profile)")
			}
			fmt.Fprintln(out, "== manifest ==")
			if !dumpFile(out, t.base.ManifestPath(t.slug)) {
				fmt.Fprintln(out, "(no manifest)")
			}
			return nil
		},
	}
	bindExisting(cmd, &f)
	return cmd
}

func readMeta(path string) (exit, secs string) {
	exit, secs = "-", "-"
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "exit="); ok {
			exit = v
		} else if v, ok := strings.CutPrefix(line, "secs="); ok {
			secs = v
		}
	}
	return
}

// jsonResult extracts the agent's result/error string from its output log.
func jsonResult(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		return ""
	}
	for _, k := range []string{"result", "error"} {
		if v, ok := m[k]; ok {
			return strings.Join(strings.Fields(fmt.Sprint(v)), " ")
		}
	}
	return ""
}

func dumpFile(out interface{ Write([]byte) (int, error) }, path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	_, _ = out.Write(data)
	if len(data) > 0 && data[len(data)-1] != '\n' {
		_, _ = out.Write([]byte("\n"))
	}
	return true
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
