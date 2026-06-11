package cli

import (
	"github.com/spf13/cobra"
)

func init() { register(newCompletionCmd) }

func newCompletionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion [bash|zsh|fish]",
		Short: "Generate shell completion script",
		Long: `Generate a shell completion script for sandboxer.

To load completions:

  bash:
    source <(sandboxer completion bash)
    # Permanent: sandboxer completion bash > ~/.bash_completion.d/sandboxer

  zsh:
    source <(sandboxer completion zsh)
    # Permanent: sandboxer completion zsh > ~/.zsh/completions/_sandboxer

  fish:
    sandboxer completion fish > ~/.config/fish/completions/sandboxer.fish`,
		ValidArgs: []string{"bash", "zsh", "fish"},
		Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			var err error
			switch args[0] {
			case "bash":
				err = cmd.Root().GenBashCompletionV2(cmd.OutOrStdout(), true)
			case "zsh":
				err = cmd.Root().GenZshCompletion(cmd.OutOrStdout())
			case "fish":
				err = cmd.Root().GenFishCompletion(cmd.OutOrStdout(), true)
			}
			return err
		},
	}
	return cmd
}
