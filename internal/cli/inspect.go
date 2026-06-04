package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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
	fmt.Fprintf(out, "%-2s %-16s %-9s %-5s %s\n", "", "SANDBOX", "EXIT", "SEC", "RESULT")
	for _, slug := range base.Agents() {
		exit, secs := readMeta(base.MetaFilePath(slug))
		res := jsonResult(base.LogPath(slug, "json"))
		marker := ""
		if slug == cur {
			marker = "*"
		}
		fmt.Fprintf(out, "%-2s %-16s %-9s %-5s %s\n",
			marker, truncate(slug, 16), exit, secs, truncate(res, 50))
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "* = active (use). enter <s> | exec <s> -- cmd | diff [s] | push [s] | rm <s>")
}

// sandboxDiff shows what changed in a sandbox's pulled deps versus their
// origins (one `diff -ruN` per manifest entry). Empty when there is no manifest.
func sandboxDiff(base *sandbox.Base, slug string) string {
	data, err := os.ReadFile(base.ManifestPath(slug))
	if err != nil {
		return ""
	}
	var entries []struct {
		Origin      string `json:"origin"`
		SandboxPath string `json:"sandboxPath"`
	}
	if json.Unmarshal(data, &entries) != nil {
		return ""
	}
	var b strings.Builder
	for _, e := range entries {
		if e.Origin == "" || e.SandboxPath == "" {
			continue
		}
		// diff exits 1 when files differ; the diff text is still on stdout.
		o, _ := exec.Command("diff", "-ruN", e.Origin, e.SandboxPath).Output()
		b.Write(o)
	}
	return b.String()
}

func newDiffCmd() *cobra.Command {
	var src string
	cmd := &cobra.Command{
		Use:   "diff [slug]",
		Short: "Show what changed in a sandbox's deps versus their origins",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := baseOnly(src)
			if err != nil {
				return err
			}
			if _, err := exec.LookPath("diff"); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "sandboxer: diff(1) not found on PATH — cannot show changes")
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
				// Only print a section when there is an actual change.
				if d := sandboxDiff(base, slug); d != "" {
					fmt.Fprintf(out, "===== %s =====\n", slug)
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
			fmt.Fprintln(out, configLine(t.runtime(f), t.slug, t.profile))
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
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
