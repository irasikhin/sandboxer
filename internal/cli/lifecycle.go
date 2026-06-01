package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/backend"
	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/srcs"
)

func init() {
	register(newCreateCmd)
	register(newEnterCmd)
	register(newExecCmd)
}

func newCreateCmd() *cobra.Command {
	var f commonFlags
	cmd := &cobra.Command{
		Use:   "create [slug|profile.yaml]",
		Short: "Create a sandbox and pull its srcs (nothing else is copied)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := resolveTarget(f, posArg(args))
			if err != nil {
				return err
			}
			if f.domains != "" {
				if err := t.base.SetDomains(f.domains); err != nil {
					return err
				}
			}
			if t.json != nil {
				if err := t.base.WriteProfileJSON(t.slug, t.json); err != nil {
					return err
				}
			}
			if err := t.base.MakeSandbox(t.slug, cmd.ErrOrStderr()); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "sandbox %q created: %s\n", t.slug, t.base.SandboxDir(t.slug))
			fmt.Fprintf(out, "enter:  sandboxer enter %s\n", t.slug)
			fmt.Fprintf(out, "run:    sandboxer exec %s -- claude\n", t.slug)
			fmt.Fprintf(out, "return: sandboxer push %s\n", t.slug)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&f.src, "src", "", "project root")
	fl.StringVar(&f.config, "config", "", "profile file (sandboxer.yaml)")
	fl.StringVar(&f.domains, "allow-domains", "", "egress allowlist (csv)")
	return cmd
}

func newEnterCmd() *cobra.Command {
	var f commonFlags
	cmd := &cobra.Command{
		Use:   "enter [slug|profile.yaml]",
		Short: "Open an interactive shell inside the sandbox",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := resolveTarget(f, posArg(args))
			if err != nil {
				return err
			}
			dest := t.base.SandboxDir(t.slug)
			if !fileExists(dest) {
				fmt.Fprintf(cmd.ErrOrStderr(), "sandbox %q does not exist — creating\n", t.slug)
				if t.json != nil {
					_ = t.base.WriteProfileJSON(t.slug, t.json)
				}
				if err := t.base.MakeSandbox(t.slug, cmd.ErrOrStderr()); err != nil {
					return err
				}
			}
			rt := t.runtime(f)
			errOut := cmd.ErrOrStderr()
			if rt.Backend == "native" {
				fmt.Fprintf(errOut, "sandboxer: shell in copy %s (backend=native; OS sandbox only when running 'claude --settings').\n", dest)
				if err := backend.NativeEnter(dest, rt, cmd.InOrStdin(), cmd.OutOrStdout(), errOut); err != nil {
					return silentErr{err}
				}
			} else {
				engine, err := backend.DetectEngine(config.LoadDefaults())
				if err != nil {
					return err
				}
				fmt.Fprintf(errOut, "sandboxer: interactive shell in container (%s %s). Agents in PATH.\n", engine, config.LoadDefaults().Image)
				_, err = backend.Run(backend.RunOpts{
					Engine: engine, Image: config.LoadDefaults().Image, Dest: dest, Slug: t.slug,
					RT: rt, Profile: t.profile,
					ProfileJSONPath: t.base.ProfileJSONPath(t.slug), ManifestPath: t.base.ManifestPath(t.slug),
					Interactive: true, Args: []string{"bash", "-l"},
					Stdin: cmd.InOrStdin(), Stdout: cmd.OutOrStdout(), Stderr: errOut,
				})
				if err != nil {
					return err
				}
			}
			pushDeps(t, cmd)
			fmt.Fprintf(errOut, "sandboxer: done in %s. Return rw srcs: sandboxer push %s\n", dest, t.slug)
			return nil
		},
	}
	bindExisting(cmd, &f)
	return cmd
}

func newExecCmd() *cobra.Command {
	var f commonFlags
	cmd := &cobra.Command{
		Use:   "exec [slug] -- <cmd...>",
		Short: "Run a command inside the sandbox",
		RunE: func(cmd *cobra.Command, args []string) error {
			pos, rest := splitDash(cmd, args)
			if len(rest) == 0 {
				return fmt.Errorf("give a command after --: sandboxer exec <slug> -- <cmd...>")
			}
			t, err := resolveTarget(f, pos)
			if err != nil {
				return err
			}
			dest := t.base.SandboxDir(t.slug)
			if !fileExists(dest) {
				return fmt.Errorf("no sandbox %q (create it: sandboxer create)", t.slug)
			}
			rt := t.runtime(f)
			if rt.Backend == "native" {
				code, err := backend.NativeExec(dest, rt, rest, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
				pushDeps(t, cmd)
				if err != nil {
					return err
				}
				if code != 0 {
					return silentErr{fmt.Errorf("command exited %d", code)}
				}
				return nil
			}
			engine, err := backend.DetectEngine(config.LoadDefaults())
			if err != nil {
				return err
			}
			code, err := backend.Run(backend.RunOpts{
				Engine: engine, Image: config.LoadDefaults().Image, Dest: dest, Slug: t.slug,
				RT: rt, Profile: t.profile,
				ProfileJSONPath: t.base.ProfileJSONPath(t.slug), ManifestPath: t.base.ManifestPath(t.slug),
				Interactive: true, Args: rest,
				Stdin: cmd.InOrStdin(), Stdout: cmd.OutOrStdout(), Stderr: cmd.ErrOrStderr(),
			})
			pushDeps(t, cmd)
			if err != nil {
				return err
			}
			if code != 0 {
				return silentErr{fmt.Errorf("command exited %d", code)}
			}
			return nil
		},
	}
	bindExisting(cmd, &f)
	return cmd
}

// pushDeps pushes rw dependencies back to their origins (if a manifest exists).
// The sandbox copy itself is returned to the source with `sandboxer return`.
func pushDeps(t *target, cmd *cobra.Command) {
	mf := t.base.ManifestPath(t.slug)
	if fileExists(mf) {
		_ = srcs.CopyOut(cmd.ErrOrStderr(), mf, false)
	}
}

// splitDash separates the optional positional before `--` from the command
// after it.
func splitDash(cmd *cobra.Command, args []string) (pos string, rest []string) {
	dash := cmd.ArgsLenAtDash()
	if dash < 0 {
		// No `--`: treat the first arg (if any) as the slug, nothing to run.
		return posArg(args), nil
	}
	before := args[:dash]
	rest = args[dash:]
	if len(before) > 0 {
		pos = before[0]
	}
	return pos, rest
}
