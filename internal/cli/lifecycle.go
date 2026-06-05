package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/backend"
	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/sandbox"
	"github.com/irasikhin/sandboxer/internal/srcs"
)

// announceFreshState prints a one-time notice when this command initialised the
// .sandboxer state tree, so the auto-created directory is never a surprise.
func announceFreshState(cmd *cobra.Command, fresh bool, root string) {
	if fresh {
		fmt.Fprintf(cmd.ErrOrStderr(), "sandboxer: initialized state in %s\n",
			filepath.Join(root, config.StateDirName))
	}
}

func init() {
	register(newCreateCmd)
	register(newEnterCmd)
	register(newExecCmd)
}

func newCreateCmd() *cobra.Command {
	var f commonFlags
	cmd := &cobra.Command{
		Use:   "create [slug|profile|file.yaml]",
		Short: "Create a sandbox and pull its deps (nothing else is copied)",
		Example: `  # named sandbox (empty unless a profile lists deps)
  sandboxer create feat

  # from a profile file — slug comes from the profile's name:
  sandboxer create ./sandboxer.yaml

  # from a named profile in the store (~/.config/sandboxer/profiles)
  sandboxer create web

  # pick a profile out of a directory
  sandboxer create web -f ./envs`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := maybeAutoScaffold(cmd, &f, posArg(args)); err != nil {
				return err
			}
			fresh := !sandbox.RunEnvExists(firstNonEmpty(f.src, getwd()))
			t, err := resolveTarget(f, posArg(args))
			if err != nil {
				return err
			}
			announceFreshState(cmd, fresh, t.base.Src)
			if f.domains != "" {
				if err := t.base.SetDomains(f.domains); err != nil {
					return err
				}
			}
			if t.profile == nil {
				return fmt.Errorf("no profile for %q — scaffold one with 'sandboxer init', then re-create", t.slug)
			}
			if t.json != nil {
				if err := t.base.WriteProfileJSON(t.slug, t.json); err != nil {
					return err
				}
			}
			if err := t.base.MakeSandbox(t.slug, cmd.ErrOrStderr()); err != nil {
				return err
			}
			rtCreate, err := t.runtime(f)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.ErrOrStderr(), configLine(rtCreate, t.slug, t.profile, backendLabel(rtCreate)))
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
	fl.StringVarP(&f.config, "config", "f", "", "profile: a file, a directory of profiles, or a named profile (store: ~/.config/sandboxer/profiles)")
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
			if err := maybeAutoScaffold(cmd, &f, posArg(args)); err != nil {
				return err
			}
			fresh := !sandbox.RunEnvExists(firstNonEmpty(f.src, getwd()))
			t, err := resolveTarget(f, posArg(args))
			if err != nil {
				return err
			}
			announceFreshState(cmd, fresh, t.base.Src)
			dest := t.base.SandboxDir(t.slug)
			if !fileExists(dest) {
				fmt.Fprintf(cmd.ErrOrStderr(), "sandbox %q does not exist — creating\n", t.slug)
				if t.json != nil {
					if err := t.base.WriteProfileJSON(t.slug, t.json); err != nil {
						return err
					}
				}
				if err := t.base.MakeSandbox(t.slug, cmd.ErrOrStderr()); err != nil {
					return err
				}
			}
			rt, rtErr := t.runtime(f)
			if rtErr != nil {
				return rtErr
			}
			if err := config.ValidateNative(rt); err != nil {
				return err
			}
			errOut := cmd.ErrOrStderr()
			fmt.Fprintln(errOut, configLine(rt, t.slug, t.profile, backendLabel(rt)))
			var runErr error
			code := 0
			if rt.Backend == "native" {
				fmt.Fprintf(errOut, "sandboxer: shell in copy %s (backend=native; OS sandbox only when running 'claude --settings').\n", dest)
				runErr = backend.NativeEnter(dest, rt, cmd.InOrStdin(), cmd.OutOrStdout(), errOut)
			} else {
				engine, err := backend.ResolveEngine(rt.Backend, config.LoadDefaults())
				if err != nil {
					return err
				}
				fmt.Fprintf(errOut, "sandboxer: interactive shell in container (%s %s). Agents in PATH.\n", engine, config.LoadDefaults().Image)
				code, runErr = backend.Run(backend.RunOpts{
					Engine: engine, Image: config.LoadDefaults().Image, Dest: dest, Slug: t.slug,
					RT: rt, Profile: t.profile,
					ProfileJSONPath: t.base.ProfileJSONPath(t.slug), ManifestPath: t.base.ManifestPath(t.slug),
					Interactive: true, Args: []string{"bash", "-l"},
					NoEgress: noEgress(),
					Stdin:    cmd.InOrStdin(), Stdout: cmd.OutOrStdout(), Stderr: errOut,
				})
			}
			// Always return rw deps, even when the shell/run failed. pushDeps (via
			// srcs.CopyOut) prints what it actually returned, so we just note we're done.
			pushErr := pushDeps(t, cmd)
			fmt.Fprintf(errOut, "sandboxer: done in %s\n", dest)
			if runErr != nil {
				return silentErr{runErr}
			}
			if code != 0 {
				return silentErr{fmt.Errorf("shell exited %d", code)}
			}
			if pushErr != nil {
				return silentErr{pushErr}
			}
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
		Example: `  # run a one-off command (note the -- separator)
  sandboxer exec feat -- npm test

  # launch an agent inside the sandbox
  sandboxer exec feat -- claude`,
		RunE: func(cmd *cobra.Command, args []string) error {
			pos, rest := splitDash(cmd, args)
			if len(rest) == 0 {
				hint := "use -- before the command, e.g.: sandboxer exec feat -- npm test"
				if pos != "" && cmd.ArgsLenAtDash() < 0 && len(args) > 1 {
					// The user likely wrote 'sandboxer exec feat npm test' (no --).
					return fmt.Errorf("no command to run — %s", hint)
				}
				return fmt.Errorf("no command to run — %s", hint)
			}
			t, err := resolveTarget(f, pos)
			if err != nil {
				return err
			}
			dest := t.base.SandboxDir(t.slug)
			if !fileExists(dest) {
				return fmt.Errorf("no sandbox %q (create it: sandboxer create)", t.slug)
			}
			rt, rtErr := t.runtime(f)
			if rtErr != nil {
				return rtErr
			}
			if err := config.ValidateNative(rt); err != nil {
				return err
			}
			fmt.Fprintln(cmd.ErrOrStderr(), configLine(rt, t.slug, t.profile, backendLabel(rt)))
			if rt.Backend == "native" {
				code, err := backend.NativeExec(dest, rt, rest, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr())
				pushErr := pushDeps(t, cmd)
				if err != nil {
					return err
				}
				if code != 0 {
					return silentErr{fmt.Errorf("command exited %d", code)}
				}
				if pushErr != nil {
					return silentErr{pushErr}
				}
				return nil
			}
			engine, err := backend.ResolveEngine(rt.Backend, config.LoadDefaults())
			if err != nil {
				return err
			}
			code, err := backend.Run(backend.RunOpts{
				Engine: engine, Image: config.LoadDefaults().Image, Dest: dest, Slug: t.slug,
				RT: rt, Profile: t.profile,
				ProfileJSONPath: t.base.ProfileJSONPath(t.slug), ManifestPath: t.base.ManifestPath(t.slug),
				Interactive: true, Args: rest,
				NoEgress: noEgress(),
				Stdin:    cmd.InOrStdin(), Stdout: cmd.OutOrStdout(), Stderr: cmd.ErrOrStderr(),
			})
			pushErr := pushDeps(t, cmd)
			if err != nil {
				return err
			}
			if code != 0 {
				return silentErr{fmt.Errorf("command exited %d", code)}
			}
			if pushErr != nil {
				return silentErr{pushErr}
			}
			return nil
		},
	}
	bindExisting(cmd, &f)
	return cmd
}

// pushDeps pushes rw dependencies back to their origins (if a manifest exists),
// the same copy-back that `sandboxer push` performs. The error is returned (not
// just printed) so the caller can exit non-zero — otherwise the user could
// believe work was returned when the copy-back actually failed.
func pushDeps(t *target, cmd *cobra.Command) error {
	mf := t.base.ManifestPath(t.slug)
	if !fileExists(mf) {
		return nil
	}
	if err := srcs.CopyOut(cmd.ErrOrStderr(), mf, false); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "sandboxer: push failed: %v\n", err)
		return err
	}
	return nil
}

// noEgress reports whether the egress allowlist is disabled via the environment
// (parity with the batch runner, which honours the same switch).
func noEgress() bool { return os.Getenv("SANDBOXER_NO_EGRESS") == "1" }

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
