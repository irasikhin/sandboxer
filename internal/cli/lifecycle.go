package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/backend"
	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/sandbox"
	"github.com/irasikhin/sandboxer/internal/worktree"
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
		Short: "Create a sandbox (a git worktree on branch sandbox/<slug>)",
		Example: `  # named sandbox (the whole repo unless a profile narrows it with deps)
  sandboxer create feat

  # from a profile file — slug comes from the profile's name:
  sandboxer create ./.sandboxer/config.yaml

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
			warnIgnoredConfig(cmd.ErrOrStderr(), t.base.Src)
			rtCreate, err := t.runtime(f)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.ErrOrStderr(), configLine(rtCreate, t.slug, t.profile, backendLabel(rtCreate)))
			warnIgnoredRoutes(cmd.ErrOrStderr(), rtCreate)
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "sandbox %q created: %s\n", t.slug, t.base.SandboxDir(t.slug))
			fmt.Fprintf(out, "enter:  sandboxer enter %s\n", t.slug)
			fmt.Fprintf(out, "run:    sandboxer exec %s -- claude\n", t.slug)
			fmt.Fprintf(out, "review: git -C %s log %s\n", t.base.RepoRoot, worktree.Branch(t.slug))
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
	var sessionName string
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
			} else if err := t.syncSnapshot(); err != nil {
				return err
			}
			rt, rtErr := t.runtime(f)
			if rtErr != nil {
				return rtErr
			}
			if err := config.ValidateBackend(rt); err != nil {
				return err
			}
			if err := config.ValidateSession(rt); err != nil {
				return err
			}
			persistent := rt.Session == config.SessionPersistent
			if persistent {
				// The name is interpolated into the bash -c launcher — reject
				// anything outside the safe alphabet before any work happens.
				if err := validateSessionName(sessionName); err != nil {
					return err
				}
			}
			errOut := cmd.ErrOrStderr()
			fmt.Fprintln(errOut, configLine(rt, t.slug, t.profile, backendLabel(rt)))
			warnIgnoredRoutes(errOut, rt)
			engine, err := backend.ResolveEngine(rt.Backend, config.LoadDefaults())
			if err != nil {
				return err
			}
			if err := t.base.EnsureHome(t.slug); err != nil {
				return err
			}
			if err := runSetup(t, rt, engine, f.noSetup, errOut); err != nil {
				return err
			}
			name := backend.SessionName(t.slug, t.base.Dir)
			if persistent {
				fmt.Fprintln(errOut, persistentEnterBanner(t.slug, engine, dest, name))
			} else {
				fmt.Fprintln(errOut, enterBanner(t.slug, engine, dest))
			}
			image, spec, err := resolveImage(t.profile, engine, errOut)
			if err != nil {
				return err
			}
			o := backend.RunOpts{
				Engine: engine, Image: image, Spec: spec, Dest: dest, Slug: t.slug,
				GitCommonDir: t.base.GitDir,
				GitUserName:  t.base.GitUserName,
				GitUserEmail: t.base.GitUserEmail,
				HomeDir:      t.base.HomeDir(t.slug),
				RT:           rt, Profile: t.profile,
				ProfileJSONPath: t.base.ProfileJSONPath(t.slug),
				Mem:             rt.Mem, CPU: rt.CPU, Pids: rt.Pids,
				Interactive: true, Args: interactiveShellArgs(),
				NoEgress: noEgress(),
				Stdin:    cmd.InOrStdin(), Stdout: cmd.OutOrStdout(), Stderr: errOut,
			}
			var code int
			var runErr error
			if persistent {
				o.BaseDir = t.base.Dir
				if _, err := backendEnsureSession(o); err != nil {
					// EnsureSession's message IS the diagnostic (busy session, egress
					// failure, …) — print it here; the tail then returns silently.
					fmt.Fprintln(errOut, "sandboxer:", err)
					runErr = err
				} else {
					code, runErr = backendExecSession(o, name, tmuxEnterArgs(sessionName))
				}
			} else {
				code, runErr = backendRun(o)
			}
			br := worktree.Branch(t.slug)
			fmt.Fprintf(errOut, "sandboxer: work is on branch %s — review with: git -C %s log %s\n",
				br, t.base.RepoRoot, br)
			fmt.Fprintf(errOut, "sandboxer: done in %s\n", dest)
			if runErr != nil {
				return silentErr{runErr}
			}
			if code != 0 {
				return silentErr{fmt.Errorf("shell exited %d", code)}
			}
			return nil
		},
	}
	bindExisting(cmd, &f)
	cmd.Flags().BoolVar(&f.noSetup, "no-setup", false, "skip the profile's one-time setup script")
	cmd.Flags().BoolVar(&f.ephemeral, "ephemeral", false, "one-shot container instead of the persistent session")
	cmd.Flags().StringVar(&sessionName, "session", "main", "tmux session name inside the persistent container")
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
			if err := t.syncSnapshot(); err != nil {
				return err
			}
			rt, rtErr := t.runtime(f)
			if rtErr != nil {
				return rtErr
			}
			if err := config.ValidateBackend(rt); err != nil {
				return err
			}
			if err := config.ValidateSession(rt); err != nil {
				return err
			}
			fmt.Fprintln(cmd.ErrOrStderr(), configLine(rt, t.slug, t.profile, backendLabel(rt)))
			warnIgnoredRoutes(cmd.ErrOrStderr(), rt)
			engine, err := backend.ResolveEngine(rt.Backend, config.LoadDefaults())
			if err != nil {
				return err
			}
			if err := t.base.EnsureHome(t.slug); err != nil {
				return err
			}
			if err := runSetup(t, rt, engine, f.noSetup, cmd.ErrOrStderr()); err != nil {
				return err
			}
			image, spec, err := resolveImage(t.profile, engine, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			o := backend.RunOpts{
				Engine: engine, Image: image, Spec: spec, Dest: dest, Slug: t.slug,
				GitCommonDir: t.base.GitDir,
				GitUserName:  t.base.GitUserName,
				GitUserEmail: t.base.GitUserEmail,
				HomeDir:      t.base.HomeDir(t.slug),
				RT:           rt, Profile: t.profile,
				ProfileJSONPath: t.base.ProfileJSONPath(t.slug),
				Mem:             rt.Mem, CPU: rt.CPU, Pids: rt.Pids,
				Interactive: true, Args: rest,
				NoEgress: noEgress(),
				Stdin:    cmd.InOrStdin(), Stdout: cmd.OutOrStdout(), Stderr: cmd.ErrOrStderr(),
			}
			// exec rides an existing running+fresh session but NEVER creates or
			// replaces the daemon container — that is enter's job. Anything else
			// (no session, stopped, stale — by profile hash or by a rebuilt
			// image under the same tag) falls back to a one-shot run.
			useSession := false
			var name string
			if rt.Session == config.SessionPersistent {
				o.BaseDir = t.base.Dir
				name = backend.SessionName(t.slug, o.BaseDir)
				if info := backendInspectSession(engine, name); info.Running {
					switch {
					case info.Hash != backendWantHash(o):
						fmt.Fprintf(cmd.ErrOrStderr(),
							"sandboxer: session %s is stale (profile changed) — running one-shot; re-enter to refresh it\n", name)
					case !backend.ImageFresh(info.ImageID, backendImageID(engine, o.Image)):
						fmt.Fprintf(cmd.ErrOrStderr(),
							"sandboxer: session %s is stale (image rebuilt) — running one-shot; re-enter to refresh it\n", name)
					default:
						useSession = true
					}
				}
			}
			var code int
			var runErr error
			if useSession {
				code, runErr = backendExecSession(o, name, rest)
			} else {
				code, runErr = backendRun(o)
			}
			if runErr != nil {
				return runErr
			}
			if code != 0 {
				return silentErr{fmt.Errorf("command exited %d", code)}
			}
			return nil
		},
	}
	bindExisting(cmd, &f)
	cmd.Flags().BoolVar(&f.noSetup, "no-setup", false, "skip the profile's one-time setup script")
	cmd.Flags().BoolVar(&f.ephemeral, "ephemeral", false, "one-shot container instead of the persistent session")
	return cmd
}

// noEgress reports whether the egress allowlist is disabled via the environment.
func noEgress() bool { return os.Getenv("SANDBOXER_NO_EGRESS") == "1" }

// warnIgnoredRoutes notes that network.routes take no effect in direct mode
// (egress off, or SANDBOXER_NO_EGRESS): routes are a property of the allowlist
// squid sidecar, which is not in the path when the agent talks to network.proxy
// directly. Best-effort advisory on the configLine path.
func warnIgnoredRoutes(w io.Writer, rt config.Runtime) {
	if len(rt.Routes) > 0 && (!rt.Egress || noEgress()) {
		fmt.Fprintln(w, "sandboxer: network.routes ignored — egress is off (routes need the allowlist sidecar; "+
			"in direct mode the agent talks to network.proxy directly)")
	}
}

// interactiveShellArgs is the in-container command for `enter`: launch bash with
// the baked rc (sandbox-aware prompt, aliases, EDITOR/PAGER) when it is present,
// otherwise a plain interactive shell. The guard tolerates an older cached
// toolbox image built before the rc existed — a fresh `sandboxer build-image`
// adds it — so flipping the launcher never strands such an image at a dead
// `--rcfile` path.
func interactiveShellArgs() []string {
	return []string{"bash", "-c",
		"test -r /etc/sandboxer/rc.sh && exec bash --rcfile /etc/sandboxer/rc.sh -i || exec bash -i"}
}

// tmuxEnterArgs is the in-container command for a persistent `enter`: attach
// to (or create) the named session on the shared `tmux -L sandboxer` server,
// under the shipped tmux config. The guard tolerates an older cached toolbox
// image built before tmux was baked in — the same convention as
// interactiveShellArgs — degrading to the plain rc shell with a rebuild hint
// instead of stranding the user. The session name is interpolated into the
// script, so callers must vet it with validateSessionName first.
func tmuxEnterArgs(session string) []string {
	return []string{"bash", "-c",
		"if command -v tmux >/dev/null && test -r /etc/sandboxer/tmux.conf; then " +
			"exec tmux -L sandboxer -f /etc/sandboxer/tmux.conf new-session -A -s " + session + "; " +
			"else " +
			"echo 'sandboxer: tmux not in image — plain shell (rebuild: sandboxer image build)' >&2; " +
			"test -r /etc/sandboxer/rc.sh && exec bash --rcfile /etc/sandboxer/rc.sh -i || exec bash -i; fi"}
}

// sessionNameRe pins --session to characters that are safe to splice into the
// bash -c launcher (the name lands inside a shell word) and that tmux accepts
// unquoted.
var sessionNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// validateSessionName rejects a tmux session name outside the safe alphabet —
// it is interpolated into tmuxEnterArgs' script, so this is an injection
// guard, not cosmetics.
func validateSessionName(name string) error {
	if !sessionNameRe.MatchString(name) {
		return fmt.Errorf("invalid --session name %q — use letters, digits, dash or underscore", name)
	}
	return nil
}

// enterBanner is the orientation notice printed to stderr when an interactive
// shell opens: which sandbox, the engine and the working directory. The
// agent's work stays on its sandbox branch — there is no copy-back. The
// backend/egress facts are already on the configLine printed just above, so
// this only adds what that line does not.
func enterBanner(slug, engine, dir string) string {
	return fmt.Sprintf(
		"sandboxer: interactive shell in %q (%s) — %s",
		slug, engine, dir)
}

// persistentEnterBanner is enterBanner's persistent-session variant: it also
// names the session container and spells out the detach semantics — detaching
// keeps the session (and anything it runs) alive.
func persistentEnterBanner(slug, engine, dir, container string) string {
	return fmt.Sprintf(
		"sandboxer: persistent session %s in %q (%s) — %s\n"+
			"sandboxer: Ctrl-q (or tmux detach) detaches — session keeps running; reattach: sandboxer enter %s",
		container, slug, engine, dir, slug)
}

// backendRun is the container-run seam, overridable in tests so the setup
// orchestration (gate → run → stamp) can be exercised without a real engine.
var backendRun = backend.Run

// Session seams beside backendRun, for the same reason: the persistent
// enter/exec orchestration is exercised in tests without a real engine.
var (
	backendEnsureSession  = backend.EnsureSession
	backendExecSession    = backend.ExecSession
	backendInspectSession = backend.InspectSession
	backendWantHash       = backend.SessionWantHash
	backendImageID        = backend.ImageID
)

// runSetup runs the profile's one-time `setup:` script inside the sandbox before
// the user/agent takes over. It is gated by a per-sandbox stamp (the script's
// hash) so it runs once and re-runs only when the script changes. Setup runs
// under the same isolation and egress allowlist as the sandbox, so network
// installs need their domains allowed. A non-zero setup is fatal by default —
// we don't drop the caller into a half-prepared sandbox — and noSetup skips it.
func runSetup(t *target, rt config.Runtime, engine string, noSetup bool, errOut io.Writer) error {
	if t.profile == nil {
		return nil
	}
	pending, hash := t.base.SetupPending(t.slug, t.profile.Setup)
	if !pending {
		return nil
	}
	if noSetup {
		fmt.Fprintf(errOut, "sandboxer: skipping setup for %q (--no-setup)\n", t.slug)
		return nil
	}
	image, spec, rerr := resolveImage(t.profile, engine, errOut)
	if rerr != nil {
		return rerr
	}
	fmt.Fprintf(errOut, "sandboxer: running setup for %q…\n", t.slug)
	code, err := backendRun(backend.RunOpts{
		Engine: engine, Image: image, Spec: spec,
		Dest: t.base.SandboxDir(t.slug), Slug: t.slug,
		GitCommonDir:    t.base.GitDir,
		GitUserName:     t.base.GitUserName,
		GitUserEmail:    t.base.GitUserEmail,
		HomeDir:         t.base.HomeDir(t.slug),
		RT:              rt,
		Profile:         t.profile,
		ProfileJSONPath: t.base.ProfileJSONPath(t.slug),
		Mem:             rt.Mem,
		CPU:             rt.CPU,
		Pids:            rt.Pids,
		Interactive:     false,
		Args:            []string{"bash", "-lc", t.profile.Setup},
		NoEgress:        noEgress(),
		Stdout:          errOut,
		Stderr:          errOut,
	})
	if err != nil {
		return fmt.Errorf("setup failed to start: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("setup exited %d — fix the `setup:` script or re-run with --no-setup", code)
	}
	return t.base.MarkSetupDone(t.slug, hash)
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
