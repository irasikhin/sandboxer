package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/config"
)

func init() { register(newReturnCmd) }

func newReturnCmd() *cobra.Command {
	var src string
	var force bool
	cmd := &cobra.Command{
		Use:     "return [slug...]",
		Aliases: []string{"merge"},
		Short:   "Copy a sandbox's changed files back to the source project",
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := baseOnly(src)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()

			list := base.Agents()
			if len(args) > 0 {
				list = nil
				for _, a := range args {
					list = append(list, config.Sanitize(a))
				}
			}
			for _, slug := range list {
				fmt.Fprintf(out, "===== %s =====\n", slug)
				if err := base.Return(slug, force, out); err != nil {
					fmt.Fprintf(out, "return[%s] failed: %v\n", slug, err)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&src, "src", "", "project root")
	cmd.Flags().BoolVar(&force, "force", false, "overwrite source files changed after create")
	return cmd
}
