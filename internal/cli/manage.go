package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/registry"
	"github.com/irasikhin/sandboxer/internal/sandbox"
)

func init() {
	register(newRmCmd)
	register(newRmAllCmd)
	register(newUseCmd)
	register(newAgentsCmd)
}

func newRmCmd() *cobra.Command {
	var f commonFlags
	cmd := &cobra.Command{
		Use:   "rm [slug]",
		Short: "Remove a sandbox and its state",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := resolveTarget(f, posArg(args))
			if err != nil {
				return err
			}
			if err := t.base.Remove(t.slug); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed sandbox: %s\n", t.slug)
			return nil
		},
	}
	bindExisting(cmd, &f)
	return cmd
}

func newRmAllCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "rm-all [src]",
		Short: "Remove the entire .sandboxer state directory",
		Long: `Remove the entire .sandboxer state directory — all sandboxes, logs and metadata
for the project. Requires --force to protect against accidental deletion;
use 'sandboxer rm <slug>' to remove a single sandbox instead.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !force {
				return fmt.Errorf("rm-all requires --force; use 'sandboxer rm <slug>' to remove a single sandbox")
			}
			src := firstNonEmpty(posArg(args), getwd())
			abs, err := filepath.Abs(src)
			if err != nil {
				return fmt.Errorf("no such directory: %s", src)
			}
			dir := filepath.Join(abs, config.StateDirName)
			if err := os.RemoveAll(dir); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed: %s\n", dir)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "required to confirm deletion")
	return cmd
}

func newUseCmd() *cobra.Command {
	var src string
	var clear bool
	cmd := &cobra.Command{
		Use:   "use [slug]",
		Short: "Get, set or clear the active sandbox",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := sandbox.ResolveBase(firstNonEmpty(src, getwd()))
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if clear {
				if err := base.ClearCurrent(); err != nil {
					return err
				}
				fmt.Fprintln(out, "active sandbox cleared")
				return nil
			}
			slug := posArg(args)
			if slug == "" {
				if cur := base.Current(); cur != "" {
					fmt.Fprintln(out, cur)
				} else {
					fmt.Fprintln(out, "(no active sandbox)")
				}
				return nil
			}
			slug = config.Sanitize(slug)
			if err := base.SetCurrent(slug); err != nil {
				return err
			}
			fmt.Fprintf(out, "active sandbox: %s\n", slug)
			return nil
		},
	}
	cmd.Flags().StringVar(&src, "src", "", "project root")
	cmd.Flags().BoolVar(&clear, "clear", false, "clear the active sandbox")
	return cmd
}

// orDash renders an empty cell as "-" so the table reads cleanly.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func newAgentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "List the agent catalog",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "AGENT\tBIN\tIMAGE\tAUTH DIRS\tENV")
			for _, name := range registry.Names() {
				a, _ := registry.Get(name)
				var dirs []string
				for _, d := range a.AuthConfigDirs {
					dirs = append(dirs, d.Path)
				}
				image := "yes"
				if a.Image != nil && !*a.Image {
					image = "no"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
					name, a.Bin, image, orDash(strings.Join(dirs, " ")), orDash(strings.Join(a.AuthEnv, ",")))
			}
			return tw.Flush()
		},
	}
	return cmd
}
