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
		Use:   "rm <slug>",
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
	cmd := &cobra.Command{
		Use:   "rm-all [src]",
		Short: "Remove the entire .sandboxer state directory",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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

func newAgentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "List the agent catalog",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			for _, name := range registry.Names() {
				a, _ := registry.Get(name)
				var dirs []string
				for _, d := range a.AuthConfigDirs {
					dirs = append(dirs, d.Path)
				}
				sandboxKind := "-"
				if a.NativeSandbox {
					sandboxKind = "native"
				}
				image := "yes"
				if a.Image != nil && !*a.Image {
					image = "no"
				}
				fmt.Fprintf(tw, "%s\tbin=%s\tsandbox=%s\timage=%s\tauth: %s\tenv: %s\n",
					name, a.Bin, sandboxKind, image, strings.Join(dirs, " "), strings.Join(a.AuthEnv, ","))
			}
			return tw.Flush()
		},
	}
	return cmd
}
