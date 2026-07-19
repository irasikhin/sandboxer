package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/backend"
	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/registry"
	"github.com/irasikhin/sandboxer/internal/sandbox"
)

// announceFreshState prints a one-time notice when this command initialised the
// project's runtime state tree, so the auto-created directory is never a
// surprise. The state lives outside the repo (config.StateDir).
func announceFreshState(cmd *cobra.Command, fresh bool, root string) {
	if fresh {
		fmt.Fprintf(cmd.ErrOrStderr(), "sandboxer: initialized state in %s\n",
			config.StateDir(root))
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
		Use:   "create [slug|profile|file.nix]",
		Short: "Create a sandbox (a git worktree per source, on the branch you configured)",
		Example: `  # named sandbox (sources = the profile's srcs; every entry names its branch)
  sandboxer create feat

  # from a profile file — slug comes from the profile's name:
  sandboxer create ./sandboxer.nix

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
				return fmt.Errorf("no profile for %q — scaffold one with 'sandboxer config init', then re-create", t.slug)
			}
			if t.json != nil {
				if err := t.base.WriteProfileJSON(t.slug, t.json); err != nil {
					return err
				}
			}
			if err := t.base.MakeSandbox(t.slug, cmd.ErrOrStderr()); err != nil {
				return err
			}
			seedHostConfigs(t, cmd.ErrOrStderr())
			warnIgnoredConfig(cmd.ErrOrStderr(), t.base.Src)
			rtCreate, err := t.runtime(f)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.ErrOrStderr(), configLine(rtCreate, t.slug, t.profile, backendLabel(rtCreate)))
			warnIgnoredRoutes(cmd.ErrOrStderr(), rtCreate)
			warnOpenNetwork(cmd.ErrOrStderr(), rtCreate, t.profile)
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "sandbox %q created: %s\n", t.slug, t.base.SandboxDir(t.slug))
			fmt.Fprintf(out, "enter:  sandboxer enter %s\n", t.slug)
			fmt.Fprintf(out, "run:    sandboxer exec %s -- claude\n", t.slug)
			for _, s := range t.base.Srcs(t.slug) {
				fmt.Fprintf(out, "review: git -C %s log %s\n", s.RepoRoot, s.Branch)
			}
			return nil
		},
	}
	// No -S/--sandbox (and no bindExisting) here: the sandbox does not exist
	// yet — its name is the positional (or the profile's name:).
	fl := cmd.Flags()
	fl.StringVar(&f.src, "src", "", "project root")
	fl.StringVarP(&f.config, "config", "f", "", "profile file (default: the project sandboxer.nix; pick a profiles section by name)")
	fl.StringVar(&f.backend, "backend", "", "backend: docker | podman")
	fl.StringVar(&f.domains, "allow-domains", "", "egress allowlist (csv)")
	return cmd
}

func newEnterCmd() *cobra.Command {
	var f commonFlags
	var sessionName string
	cmd := &cobra.Command{
		Use:   "enter [slug|profile|file.nix]",
		Short: "Open an interactive shell inside the sandbox",
		Long: `Open a shell inside the sandbox, attached to an in-container tmux
session (detach: Ctrl-b d; the session and anything running in it keep going
— a later enter drops straight back in, and a second terminal can attach the
same session in parallel). By default the shell runs in the persistent
session container; --ephemeral runs a one-shot container instead. A sandbox
that doesn't exist yet is created first.

The tmux server lives INSIDE the container (tmux -L sandboxer, system
/etc/tmux.conf: mouse on, sandboxer prompt in every pane). --session opens a
separate named session in the same container.`,
		Example: `  # enter the active sandbox (see: sandboxer use)
  sandboxer enter

  # enter (or create) the sandbox "feat"
  sandboxer enter feat

  # a second terminal into the same tmux session
  sandboxer enter feat

  # a separate tmux session in the same container
  sandboxer enter feat --session side`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateSessionName(sessionName); err != nil {
				return err
			}
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
			createdDest := !fileExists(dest)
			if createdDest {
				fmt.Fprintf(cmd.ErrOrStderr(), "sandbox %q does not exist — creating\n", t.slug)
				if t.json != nil {
					if err := t.base.WriteProfileJSON(t.slug, t.json); err != nil {
						return err
					}
				}
				if err := t.base.MakeSandbox(t.slug, cmd.ErrOrStderr()); err != nil {
					return err
				}
			} else {
				if err := t.syncSnapshot(); err != nil {
					return err
				}
				// Converge the sources onto the (possibly edited) profile: new
				// srcs materialize under <slug>/ — a live session sees them
				// immediately through its stable mount.
				if _, err := t.base.SyncSrcs(t.slug, cmd.ErrOrStderr()); err != nil {
					return err
				}
			}
			// Re-resolve after the snapshot landed: a profile worktreesDir is
			// only visible once profile.json is stored.
			dest = t.base.SandboxDir(t.slug)
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
			errOut := cmd.ErrOrStderr()
			fmt.Fprintln(errOut, configLine(rt, t.slug, t.profile, backendLabel(rt)))
			// Show what the sandbox actually exposes — one line per source repo,
			// with its branch and where the worktree lives.
			for _, s := range t.base.Srcs(t.slug) {
				fmt.Fprintf(errOut, "sandboxer: src %s\n", srcLine(s))
			}
			warnIgnoredRoutes(errOut, rt)
			warnOpenNetwork(errOut, rt, t.profile)
			engine, err := backend.ResolveEngine(rt.Backend, config.LoadDefaults())
			if err != nil {
				return err
			}
			if err := t.base.EnsureHome(t.slug); err != nil {
				return err
			}
			seedHostConfigs(t, errOut)
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
			mountDest, srcMounts, mountGen := t.mounts()
			o := backend.RunOpts{
				Engine: engine, Image: image, Spec: spec, Dest: dest, Slug: t.slug,
				MountDest: mountDest,
				MountGen:  mountGen,
				SrcMounts: srcMounts,
				HomeDir:   t.base.HomeDir(t.slug),
				DestGen:   t.base.Gen(t.slug),
				AuthEnv:   hostAuthEnv(t.profile),
				RT:        rt, Profile: t.profile,
				ProfileJSONPath: t.base.ProfileJSONPath(t.slug),
				Mem:             rt.Mem, CPU: rt.CPU, Pids: rt.Pids,
				Interactive: true, Args: tmuxEnterArgs(sessionName),
				NoEgress: noEgress(),
				Stdin:    cmd.InOrStdin(), Stdout: cmd.OutOrStdout(), Stderr: errOut,
			}
			var code int
			var runErr error
			if persistent {
				o.BaseDir = t.base.Dir
				// Safety check: if the session is running with a stale config
				// (profile changed or image rebuilt), refuse to tear it down
				// by default — fall back to a one-shot container instead, so
				// the running session (and any agent/tmux inside) stays alive.
				// --recreate forces the old behaviour. A dest this enter had to
				// (re)create is the exception: a session from before the
				// deletion bind-mounts the OLD directory — whatever runs inside
				// sees a deleted tree, so converging (recreating) it loses
				// nothing and is the only way the fresh worktrees get mounted.
				forceRecreate := f.recreate || createdDest
				if !forceRecreate {
					if info := backendInspectSession(engine, name); info.Running {
						if info.Hash != backendWantHash(o) {
							fmt.Fprintf(errOut,
								"sandboxer: session %s is stale (profile changed) — running one-shot; use --recreate to rebuild\n", name)
						} else if !backend.ImageFresh(info.ImageID, backendImageID(engine, o.Image)) {
							fmt.Fprintf(errOut,
								"sandboxer: session %s is stale (image rebuilt) — running one-shot; use --recreate to rebuild\n", name)
						} else {
							forceRecreate = true // fresh → proceed to EnsureSession
						}
					} else {
						forceRecreate = true // not running → safe to converge
					}
				}
				if forceRecreate {
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
			} else {
				code, runErr = backendRun(o)
			}
			for _, s := range t.base.Srcs(t.slug) {
				fmt.Fprintf(errOut, "sandboxer: %s: work is in %s — commit/review on the host: git -C %s log %s\n",
					filepath.Base(s.RepoRoot), s.Path, s.RepoRoot, s.Branch)
			}
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
	cmd.Flags().BoolVar(&f.recreate, "recreate", false, "force session rebuild even if running (picks up config changes)")
	cmd.Flags().StringVar(&sessionName, "session", "main", "tmux session name inside the container")
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
			if _, err := t.base.SyncSrcs(t.slug, cmd.ErrOrStderr()); err != nil {
				return err
			}
			// Re-resolve after the snapshot landed (worktreesDir may have changed).
			dest = t.base.SandboxDir(t.slug)
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
			warnOpenNetwork(cmd.ErrOrStderr(), rt, t.profile)
			engine, err := backend.ResolveEngine(rt.Backend, config.LoadDefaults())
			if err != nil {
				return err
			}
			if err := t.base.EnsureHome(t.slug); err != nil {
				return err
			}
			seedHostConfigs(t, cmd.ErrOrStderr())
			if err := runSetup(t, rt, engine, f.noSetup, cmd.ErrOrStderr()); err != nil {
				return err
			}
			image, spec, err := resolveImage(t.profile, engine, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			mountDest, srcMounts, mountGen := t.mounts()
			o := backend.RunOpts{
				Engine: engine, Image: image, Spec: spec, Dest: dest, Slug: t.slug,
				MountDest: mountDest,
				MountGen:  mountGen,
				SrcMounts: srcMounts,
				HomeDir:   t.base.HomeDir(t.slug),
				DestGen:   t.base.Gen(t.slug),
				AuthEnv:   hostAuthEnv(t.profile),
				RT:        rt, Profile: t.profile,
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

// seedHostConfigs seeds the sandbox home from the host's agent configs when
// the profile opts in (hostConfigs = true) — after EnsureHome, before anything
// runs in the container. Copy-only and never-overwrite semantics live in
// sandbox.SeedHome; this is just the profile gate.
func seedHostConfigs(t *target, w io.Writer) {
	if t.profile != nil && t.profile.HostConfigs {
		t.base.SeedHome(t.slug, w)
	}
}

// hostAuthEnv collects the registry agents' auth env vars that are set (and
// non-empty) in the HOST environment — CLAUDE_CODE_OAUTH_TOKEN, API keys — for
// the container env, gated by the same hostConfigs opt-in as the config seed.
// This is the durable half of host auth: a long-lived token survives any
// number of sandboxes, while a copied OAuth credentials file dies with the
// next refresh-token rotation (see registry seed skip). Sorted, deduped: the
// result feeds the argv that ConfigHash fingerprints.
func hostAuthEnv(p *config.Profile) []string {
	if p == nil || !p.HostConfigs {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, name := range registry.Names() {
		a, err := registry.Get(name)
		if err != nil {
			continue
		}
		for _, k := range a.AuthEnv {
			if seen[k] {
				continue
			}
			seen[k] = true
			if v := os.Getenv(k); v != "" {
				out = append(out, k+"="+v)
			}
		}
	}
	sort.Strings(out)
	return out
}

// noEgress reports whether the egress allowlist is disabled via the environment.
func noEgress() bool { return os.Getenv("SANDBOXER_NO_EGRESS") == "1" }

// warnIgnoredRoutes notes that egress.routes take no effect in direct mode
// (egress.enabled = false, or SANDBOXER_NO_EGRESS): routes are a property of the
// allowlist squid sidecar, which is not in the path when the agent talks to
// egress.proxy directly. Best-effort advisory on the configLine path.
func warnIgnoredRoutes(w io.Writer, rt config.Runtime) {
	if len(rt.Routes) > 0 && (!rt.Egress || noEgress()) {
		fmt.Fprintln(w, "sandboxer: egress.routes ignored — egress is off (routes need the allowlist sidecar; "+
			"in direct mode the agent talks to egress.proxy directly)")
	}
}

// warnOpenNetwork warns when the resolved network is fully open — no allowlist
// sidecar and no proxy (networkOpen) — so the agent has unrestricted outbound.
// The configLine already labels this "OPEN", but it is the one egress state
// with no wall at all, and a run that also seeds host credentials (hostConfigs)
// deserves an explicit line: the allowlist is the wall between those creds and
// an exfiltration attempt, and here there is none. See SECURITY.md.
func warnOpenNetwork(w io.Writer, rt config.Runtime, prof *config.Profile) {
	if !networkOpen(rt) {
		return
	}
	msg := "sandboxer: WARNING — egress is unrestricted (no allowlist, no proxy); the agent can reach any host"
	if prof != nil && prof.HostConfigs {
		msg += " — and hostConfigs is on, so seeded credentials could be exfiltrated"
	}
	fmt.Fprintln(w, msg)
}

// tmuxEnterArgs is the in-container command for `enter`: attach to (or
// create) the named session on the in-container `tmux -L sandboxer` server —
// the system /etc/tmux.conf routes every pane through the rc.sh launcher
// (sandbox-aware prompt, aliases) and turns the mouse on, so wheel scrolling
// works regardless of what TERM made it into the container. The guard
// tolerates an older cached toolbox image without tmux — degrading to the
// plain rc shell with a rebuild hint instead of stranding the user. The
// session name is interpolated into the script, so callers must vet it with
// validateSessionName first.
func tmuxEnterArgs(session string) []string {
	return []string{"bash", "-c",
		"if command -v tmux >/dev/null; then " +
			"exec tmux -L sandboxer new-session -A -s " + session + "; " +
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
// this only adds what that line does not. (The one-shot container still runs
// tmux — mouse scrolling and panes — it just dies with the shell.)
func enterBanner(slug, engine, dir string) string {
	return fmt.Sprintf(
		"sandboxer: interactive shell in %q (%s) — %s",
		slug, engine, dir)
}

// persistentEnterBanner is enterBanner's persistent-session variant: it also
// names the session container and spells out the exit semantics — detaching
// or exiting keeps the container (and the in-container tmux with everything
// in it) alive for a later re-enter.
func persistentEnterBanner(slug, engine, dir, container string) string {
	return fmt.Sprintf(
		"sandboxer: persistent session %s in %q (%s) — %s\n"+
			"sandboxer: tmux inside — Ctrl-b d detaches, exiting keeps the container running; reattach: sandboxer enter %s",
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
	mountDest, srcMounts, mountGen := t.mounts()
	code, err := backendRun(backend.RunOpts{
		Engine: engine, Image: image, Spec: spec,
		Dest: t.base.SandboxDir(t.slug), Slug: t.slug,
		MountDest:       mountDest,
		MountGen:        mountGen,
		SrcMounts:       srcMounts,
		HomeDir:         t.base.HomeDir(t.slug),
		DestGen:         t.base.Gen(t.slug),
		AuthEnv:         hostAuthEnv(t.profile),
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
