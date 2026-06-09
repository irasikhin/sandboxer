package cli

import (
	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/proxy"
)

func init() { register(newProxyCmd) }

// newProxyCmd is the hidden egress sidecar mode: the same binary, baked into
// the toolbox image, runs as the allowlist forward-proxy.
func newProxyCmd() *cobra.Command {
	var listen string
	var allow []string
	var upstream string
	cmd := &cobra.Command{
		Use:    "_proxy",
		Short:  "(internal) egress allowlist forward-proxy",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return proxy.ListenAndServeUpstream(listen, allow, upstream)
		},
	}
	cmd.Flags().StringVar(&listen, "listen", ":8888", "listen address")
	cmd.Flags().StringArrayVar(&allow, "allow", nil, "allowed domain (repeatable)")
	cmd.Flags().StringVar(&upstream, "upstream", "", "upstream proxy URL to chain allowed traffic through")
	return cmd
}
