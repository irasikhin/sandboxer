package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/gitx"
)

func init() { register(newMergeCmd) }

func newMergeCmd() *cobra.Command {
	var src string
	var patch bool
	cmd := &cobra.Command{
		Use:   "merge [slug...]",
		Short: "Return sandbox code to the source repo (cherry-pick) or emit patches",
		RunE: func(cmd *cobra.Command, args []string) error {
			base, err := baseOnly(src)
			if err != nil {
				return err
			}
			if !base.IsGit {
				return fmt.Errorf("source project is not a git repo — merge unavailable (see diff / --patch)")
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
				dest := base.SandboxDir(slug)
				baseRef := base.BaseRef(slug, base.BaseSHA)
				if patch {
					pdir := filepath.Join(base.Dir, "_patches", slug)
					_ = os.MkdirAll(pdir, 0o755)
					produced, err := gitx.FormatPatch(dest, baseRef+"..HEAD", pdir)
					if err == nil && produced {
						fmt.Fprintf(out, "patch[%s] -> %s\n", slug, pdir)
					} else {
						fmt.Fprintf(out, "patch[%s] no changes\n", slug)
					}
					continue
				}
				if err := gitx.Fetch(base.Src, dest, "sandbox/"+slug); err != nil {
					fmt.Fprintf(out, "merge[%s] fetch failed\n", slug)
					continue
				}
				tip, err := gitx.RevParse(base.Src, "FETCH_HEAD")
				if err != nil {
					fmt.Fprintf(out, "merge[%s] fetch failed\n", slug)
					continue
				}
				rng := baseRef + ".." + tip
				if gitx.RevListCount(base.Src, rng) == 0 {
					fmt.Fprintf(out, "merge[%s] no changes\n", slug)
					continue
				}
				if err := gitx.CherryPick(base.Src, rng); err == nil {
					branch, _ := gitx.CurrentBranch(base.Src)
					fmt.Fprintf(out, "merged sandbox/%s -> %s (%d commit(s))\n", slug, branch, gitx.RevListCount(base.Src, rng))
				} else {
					gitx.CherryPickAbort(base.Src)
					fmt.Fprintf(out, "merge[%s] CONFLICT — manual: git -C %s fetch %s sandbox/%s && git -C %s cherry-pick %s..FETCH_HEAD\n",
						slug, base.Src, dest, slug, base.Src, baseRef)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&src, "src", "", "project root")
	cmd.Flags().BoolVar(&patch, "patch", false, "emit patches into _patches/<slug> instead of cherry-picking")
	return cmd
}
