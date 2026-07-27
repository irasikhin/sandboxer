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

  # a named section of the project config (profiles = { web = {...}; })
  sandboxer create web

  # the same, from another config file
  sandboxer create web -f ./envs.nix`,
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
			warnMicrovmIgnored(cmd.ErrOrStderr(), rtCreate, t.profile)
			warnMicrovmProxy(cmd.ErrOrStderr(), rtCreate)
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
	fl.StringVar(&f.backend, "backend", "", "backend: docker | podman | microvm")
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
session (detach: Ctrl-Space d; the session and anything running in it keep going
— a later enter drops straight back in, and a second terminal can attach the
same session in parallel). By default the shell runs in the persistent
session container; --ephemeral runs a one-shot container instead. A sandbox
that doesn't exist yet is created first.

The tmux server lives INSIDE the container (tmux -L sandboxer, system
/etc/tmux.conf: mouse on, sandboxer prompt in every pane; the prefix is
Ctrl-Space, so Ctrl-Space c opens a new window). --session opens a separate
named session in the same container.

When a replaced container's saved layout is restored, panes that were running
a cataloged agent relaunch it with its resume command (claude --continue) —
disable with autoResume = false in the profile, or SANDBOXER_NO_RESUME=1.`,
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
			warnMicrovmIgnored(errOut, rt, t.profile)
			warnMicrovmProxy(errOut, rt)
			engine, err := backend.ResolveEngine(rt.Backend, config.LoadDefaults())
			if err != nil {
				return err
			}
			if err := t.base.EnsureHome(t.slug); err != nil {
				return err
			}
			seedHostConfigs(t, errOut)
			nestedIDs := prepareNestedIDs(t, engine, errOut)
			if err := runSetup(t, rt, engine, nestedIDs, f.noSetup, errOut); err != nil {
				return err
			}
			name := backend.SessionName(t.slug, t.base.Dir)
			image, spec, err := resolveImage(t.profile, engine, errOut)
			if err != nil {
				return err
			}
			mountDest, srcMounts, mountGen, mountIDs, err := t.mounts()
			if err != nil {
				return err
			}
			o := backend.RunOpts{
				Engine: engine, Image: image, Spec: spec, Dest: dest, Slug: t.slug,
				MountDest:        mountDest,
				MountGen:         mountGen,
				MountIDs:         mountIDs,
				SrcMounts:        srcMounts,
				HomeDir:          t.base.HomeDir(t.slug),
				SessionStatePath: t.base.SessionStatePath(t.slug),
				DestGen:          t.base.Gen(t.slug),
				AuthEnv:          hostAuthEnv(t.profile),
				RT:               rt, Profile: t.profile,
				ProfileJSONPath: t.base.ProfileJSONPath(t.slug),
				NestedIDFiles:   nestedIDs,
				Mem:             rt.Mem, CPU: rt.CPU, Pids: rt.Pids,
				Interactive: true, Args: tmuxEnterArgs(sessionName),
				NoEgress: noEgress(),
				Stdin:    cmd.InOrStdin(), Stdout: cmd.OutOrStdout(), Stderr: errOut,
			}
			// Decide the container shape BEFORE announcing it. The banner
			// promises detach semantics, and announcing first told the user
			// "exiting keeps the container running" and then handed them a
			// `run --rm` container, where detaching tmux exits the container's
			// main process and takes the whole session with it.
			useSession := false
			staleWhy := "" // non-empty: attach to the running stale session as-is
			oneShotWhy := ""
			if persistent {
				o.BaseDir = t.base.Dir
				// A session running with a stale config (profile changed or image
				// rebuilt) is never torn down silently — but it is not sidestepped
				// into a one-shot container either. That fallback handed the user a
				// `run --rm` container where Ctrl-Space d destroys everything, left
				// the real session unreachable, and — because nothing ever converged
				// it — did so again on EVERY later enter: a permanent one-shot trap.
				// So ask the session what it is holding: nothing (SessionIdle) means
				// there is nothing to lose, so converge it; a live tmux session holds
				// the user's agent and is attached as-is, with the pending config
				// called out. Either way enter lands in the session container, so
				// detaching is always safe. --recreate forces the rebuild regardless.
				// A dest this enter had to (re)create is the same exception as
				// before: a session from before the deletion bind-mounts the OLD
				// directory — whatever runs inside sees a deleted tree, so converging
				// it loses nothing and is the only way the fresh worktrees get
				// mounted.
				useSession = f.recreate || createdDest
				if !useSession {
					info := backendInspectSession(engine, name)
					drift := false
					driftDetail := ""
					switch {
					case !info.Running:
						useSession = true // not running → safe to converge
					case info.Hash != backendWantHash(o):
						staleWhy, driftDetail, drift = mountDriftWhy(o, info, mountIDs)
					case !backend.ImageFresh(info.ImageID, backendImageID(engine, o.Image)):
						staleWhy = "image rebuilt"
					default:
						useSession = true // fresh → proceed to EnsureSession
					}
					switch {
					case staleWhy == "":
					case backendSessionIdle(engine, name):
						// Holds no tmux session: converge it now (EnsureSession
						// recreates and says so) instead of stranding it forever.
						staleWhy, useSession = "", true
					case drift:
						// Busy AND the mounts moved — the one stale shape where
						// attaching as-is is not merely a postponement: the bind
						// mounts name directories the host has replaced, so what
						// is running in there is already reading the wrong tree.
						// Say which paths, once; then offer the rebuild rather
						// than deciding for the user — but only to a user who is
						// there to answer.
						fmt.Fprintln(errOut, driftDetail)
						if backendIsTerminal(o.Stdin) && confirmRecreate(o.Stdin, errOut, t.slug) {
							staleWhy, useSession = "", true
						}
					}
				}
			} else {
				oneShotWhy = ephemeralWhy(f, t.profile)
			}
			switch {
			case staleWhy != "":
				fmt.Fprintln(errOut, staleSessionEnterBanner(t.slug, engine, dest, name, staleWhy))
			case useSession:
				fmt.Fprintln(errOut, persistentEnterBanner(t.slug, engine, dest, name))
			default:
				fmt.Fprintln(errOut, oneShotEnterBanner(t.slug, engine, dest, oneShotWhy))
			}
			var code int
			var runErr error
			attached := false
			switch {
			case staleWhy != "":
				// Attach to what is already running — deliberately NOT through
				// EnsureSession, which would recreate the stale container.
				code, runErr = backendExecSession(o, name, tmuxEnterArgs(sessionName))
				attached = true
			case useSession:
				if _, err := backendEnsureSession(o); err != nil {
					// EnsureSession's message IS the diagnostic (busy session, egress
					// failure, …) — print it here; the tail then returns silently.
					fmt.Fprintln(errOut, "sandboxer:", err)
					runErr = err
				} else {
					code, runErr = backendExecSession(o, name, tmuxRestoreArgs(sessionName, o.SessionStatePath, resumeFor(rt)))
					attached = true
				}
			default:
				code, runErr = backendRun(o)
			}
			if attached {
				// The attach returned — the user detached (session still live)
				// or exited it. Refresh the saved layout NOW, not only at
				// stop/recreate, so the freshest state survives an ungraceful
				// container death (host reboot, engine restart) — and a
				// deliberate full exit resets it, keeping the exit banner's
				// "the next enter opens a fresh one" true.
				backendSyncSessionState(engine, name, o.SessionStatePath)
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
			nestedIDs := prepareNestedIDs(t, engine, cmd.ErrOrStderr())
			if err := runSetup(t, rt, engine, nestedIDs, f.noSetup, cmd.ErrOrStderr()); err != nil {
				return err
			}
			image, spec, err := resolveImage(t.profile, engine, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			mountDest, srcMounts, mountGen, mountIDs, err := t.mounts()
			if err != nil {
				return err
			}
			o := backend.RunOpts{
				Engine: engine, Image: image, Spec: spec, Dest: dest, Slug: t.slug,
				MountDest: mountDest,
				MountGen:  mountGen,
				MountIDs:  mountIDs,
				SrcMounts: srcMounts,
				HomeDir:   t.base.HomeDir(t.slug),
				DestGen:   t.base.Gen(t.slug),
				AuthEnv:   hostAuthEnv(t.profile),
				RT:        rt, Profile: t.profile,
				ProfileJSONPath: t.base.ProfileJSONPath(t.slug),
				NestedIDFiles:   nestedIDs,
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
						// Same accurate diagnosis as enter, never the prompt: a
						// one-shot already runs against the current mounts, and
						// exec has no terminal contract to ask on.
						why, detail, drift := mountDriftWhy(o, info, mountIDs)
						if drift {
							fmt.Fprintln(cmd.ErrOrStderr(), detail)
						}
						fmt.Fprintln(cmd.ErrOrStderr(), staleExecNotice(name, why, t.slug))
					case !backend.ImageFresh(info.ImageID, backendImageID(engine, o.Image)):
						fmt.Fprintln(cmd.ErrOrStderr(), staleExecNotice(name, "image rebuilt", t.slug))
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

// warnMicrovmIgnored notes the container-only knobs a microVM sandbox silently
// drops: a PID cap (smolvm has no equivalent) and nestedContainers (a real VM
// runs container engines natively, so the relaxations are moot). Egress
// features microvm cannot honor are a hard config error (ValidateBackend), not
// a warning.
func warnMicrovmIgnored(w io.Writer, rt config.Runtime, prof *config.Profile) {
	if rt.Backend != "microvm" {
		return
	}
	if rt.Pids > 0 {
		fmt.Fprintln(w, "sandboxer: limits.pids ignored — the microvm backend has no PID-count cap")
	}
	if prof != nil && prof.NestedContainers {
		fmt.Fprintln(w, "sandboxer: nestedContainers ignored — a microVM runs container engines natively")
	}
}

// warnMicrovmProxy notes the egress posture when a microvm sandbox has BOTH a
// proxy and an allowlist: the proxy is the egress path (open network at the VM
// layer), and the allowlist is enforced by the proxy rather than by smolvm's
// --allow-host — because reaching a host-local proxy needs the open TSI network,
// which cannot also filter at the network layer. The user should ensure their
// proxy enforces the intended allowlist. Not an error: a proxy + a default
// allowlist is the common case.
func warnMicrovmProxy(w io.Writer, rt config.Runtime) {
	if rt.Backend != "microvm" || rt.Proxy == "" || len(rt.Domains) == 0 {
		return
	}
	fmt.Fprintln(w, "sandboxer: egress.proxy is set — the agent's egress goes through the proxy over "+
		"an open VM network; egress.allowedDomains is then enforced by the proxy, not by the microVM. "+
		"Make sure the proxy restricts egress to the intended allowlist.")
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

// tmuxLaunch wraps an in-container launch command (the then-branch) with the
// tmux-presence guard and the plain-shell fallback, so both a fresh attach and
// a layout restore degrade identically on an older cached toolbox image that
// predates tmux — the system /etc/tmux.conf routes every pane through the rc.sh
// launcher (sandbox-aware prompt, aliases, mouse on) when tmux is there, and the
// rc shell with a rebuild hint when it is not. primary must NOT end in a newline
// (it is spliced in before `; else`), and any value interpolated into it must be
// shell-safe — callers vet a session name with validateSessionName first.
func tmuxLaunch(primary string) []string {
	return []string{"bash", "-c",
		"if command -v tmux >/dev/null; then " + primary + "; else " +
			"echo 'sandboxer: tmux not in image — plain shell (rebuild: sandboxer image build)' >&2; " +
			"test -r /etc/sandboxer/rc.sh && exec bash --rcfile /etc/sandboxer/rc.sh -i || exec bash -i; fi"}
}

// tmuxEnterArgs is the in-container command for `enter`: attach to (or create)
// the named session on the in-container `tmux -L sandboxer` server.
func tmuxEnterArgs(session string) []string {
	return tmuxLaunch("exec tmux -L sandboxer new-session -A -s " + session)
}

// tmuxRestoreArgs is tmuxEnterArgs plus a one-time layout restore: when a saved
// session state exists at statePath (written before this container replaced the
// last one), the attach first rebuilds the recorded windows/panes and then
// attaches. The rebuild is idempotent — a live session is left untouched by the
// `has-session` guard baked into the script — so with no saved state it is
// exactly tmuxEnterArgs, and callers use it unconditionally. The saved state is
// NOT consumed here (it is deleted only by rm/clean): a fresh container has no
// live session, so it rebuilds; a re-enter into a live one skips straight to the
// attach. resume maps a recorded pane agent to its relaunch commands
// (resumeFor); nil restores layout only.
func tmuxRestoreArgs(session, statePath string, resume map[string]registry.ResumeSpec) []string {
	saved := backend.ReadTmuxState(statePath)
	if len(saved) == 0 {
		return tmuxEnterArgs(session)
	}
	return tmuxLaunch(backend.TmuxRestoreScript(saved, session, resume))
}

// resumeFor gates the registry's resume catalog on the resolved runtime: nil —
// layout-only restore — when auto-resume is off (profile autoResume = false,
// or the SANDBOXER_NO_RESUME=1 kill-switch).
func resumeFor(rt config.Runtime) map[string]registry.ResumeSpec {
	if !rt.AutoResume {
		return nil
	}
	return registry.ResumeMap()
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

// persistentEnterBanner names the session container and separates the two
// ways out, because they are NOT equivalent and conflating them cost a user
// their session: detaching leaves the tmux session running, while exiting the
// shell closes its last pane — which ends the tmux session, and with tmux's
// default exit-empty the whole in-container server. The CONTAINER survives
// either way, which is what the old one-liner said, and that half-truth read
// as "exiting is safe too". Only printed once the session container is
// actually the thing about to run (see enter's useSession).
func persistentEnterBanner(slug, engine, dir, container string) string {
	return fmt.Sprintf(
		"sandboxer: persistent session %s in %q (%s) — %s\n"+
			"sandboxer: Ctrl-Space d DETACHES — the tmux session and everything in it keep running; reattach: sandboxer enter %s\n"+
			"sandboxer: exiting the shell ENDS that tmux session (the container itself stays) — the next enter opens a fresh one",
		container, slug, engine, dir, slug)
}

// staleSessionEnterBanner covers the one case where enter attaches to a
// session it cannot converge: the container is running with a stale config but
// holds a live tmux session, so rebuilding it would kill whatever is in there.
// Detach semantics are the persistent ones (that is the whole point — the user
// lands in their real session), and the pending config is stated with the one
// command that applies it, so "my change did nothing" is never a mystery.
func staleSessionEnterBanner(slug, engine, dir, container, why string) string {
	return fmt.Sprintf(
		"sandboxer: persistent session %s in %q (%s) — %s\n"+
			"sandboxer: attaching as-is: the session is stale (%s) but is running a tmux session — not rebuilding it under you\n"+
			"sandboxer: the new configuration applies once it is empty, or now: sandboxer stop %s && sandboxer enter %s\n"+
			"sandboxer: Ctrl-Space d DETACHES — the tmux session and everything in it keep running; reattach: sandboxer enter %s\n"+
			"sandboxer: exiting the shell ENDS that tmux session (the container itself stays) — the next enter opens a fresh one",
		container, slug, engine, dir, why, slug, slug, slug)
}

// staleExecNotice explains exec's one-shot fallback for a stale session. A
// single command in a throwaway container costs nothing and runs with the
// configuration just asked for, so the fallback is right here — but the old
// wording ("re-enter to refresh it") promised more than enter delivers: a
// session still holding a tmux session is attached, not rebuilt. Name both
// ways it actually refreshes.
func staleExecNotice(container, why, slug string) string {
	return fmt.Sprintf("sandboxer: session %s is stale (%s) — running one-shot; it refreshes on the next enter"+
		" once that session is empty, or now: sandboxer stop %s && sandboxer enter %s",
		container, why, slug, slug)
}

// oneShotEnterBanner is the honest banner for a `run --rm` container: there,
// tmux is the container's MAIN process, so detaching (Ctrl-Space d) exits it
// and the container — and the session — go with it. That is the opposite of
// what the persistent banner promises, which is why the two are chosen only
// after the mode is decided. why names which ephemeral switch is in force —
// the persistent path never lands here: a stale session is converged or
// attached, never sidestepped into a container detach would destroy.
func oneShotEnterBanner(slug, engine, dir, why string) string {
	b := fmt.Sprintf("sandboxer: one-shot container in %q (%s) — %s\n", slug, engine, dir)
	if why != "" {
		b += "sandboxer: " + why + "\n"
	}
	return b + "sandboxer: this container is --rm: Ctrl-Space d or exiting ENDS it and everything running in it" +
		" (committed and uncommitted work in the source worktrees is on the host and survives)"
}

// ephemeralWhy names WHICH of the three ephemeral switches is in force, in the
// precedence order config.ResolveRuntime applies (flag, then the env
// kill-switch, then the profile). Without it a one-shot container is a silent
// surprise when the switch lives somewhere the user is not looking — an
// exported SANDBOXER_SESSION outranks the profile.
func ephemeralWhy(f commonFlags, prof *config.Profile) string {
	switch {
	case f.ephemeral:
		return "ephemeral mode (--ephemeral)"
	case config.LoadDefaults().Session == config.SessionEphemeral:
		return "ephemeral mode (SANDBOXER_SESSION=ephemeral in the environment)"
	case prof != nil && prof.Session == config.SessionEphemeral:
		return `ephemeral mode (session = "ephemeral" in the profile)`
	}
	return "ephemeral mode"
}

// backendRun is the container-run seam, overridable in tests so the setup
// orchestration (gate → run → stamp) can be exercised without a real engine.
var backendRun = backend.Run

// Session seams beside backendRun, for the same reason: the persistent
// enter/exec orchestration is exercised in tests without a real engine.
var (
	backendEnsureSession    = backend.EnsureSession
	backendExecSession      = backend.ExecSession
	backendInspectSession   = backend.InspectSession
	backendSessionIdle      = backend.SessionIdle
	backendSyncSessionState = backend.SyncSessionState
	backendWantHash         = backend.SessionWantHash
	backendImageID          = backend.ImageID
	// backendIsTerminal gates the one interactive prompt (confirmRecreate).
	// A seam because the alternative is a pty: the real check wants an
	// *os.File whose mode says character device, and the tests' stdin is a
	// string reader — which is exactly the "never ask" case, so without the
	// seam the prompt's ANSWER paths could not be exercised at all.
	backendIsTerminal = backend.IsInteractiveTerminal
	// sandboxWriteNestedIDs is the id-file generation seam: the real thing
	// reads the HOST's /etc/subuid, whose content a test cannot control.
	sandboxWriteNestedIDs = (*sandbox.Base).WriteNestedIDFiles
)

// prepareNestedIDs generates the per-sandbox identity files a nestedContainers
// profile mounts over /etc/{passwd,group,subuid,subgid} (multi-uid nested
// podman — see backend.nestedContainerArgs) and returns their paths for
// RunOpts. A no-op zero value for every other profile. Generation failing, or
// finding no subordinate ranges on the host, is never fatal: the sandbox still
// runs, the nested podman just stays single-uid — but on a podman engine,
// where multi-uid WOULD have worked, the missing host ranges are worth a
// pointer at the fix.
func prepareNestedIDs(t *target, engine string, errOut io.Writer) backend.NestedIDFiles {
	if t.profile == nil || !t.profile.NestedContainers || inContainer() {
		return backend.NestedIDFiles{}
	}
	ok, err := sandboxWriteNestedIDs(t.base, t.slug)
	if err != nil {
		fmt.Fprintf(errOut, "sandboxer: nested id files: %v (nested podman stays single-uid)\n", err)
		return backend.NestedIDFiles{}
	}
	if !ok {
		if engine == "podman" {
			fmt.Fprintln(errOut, "sandboxer: no subordinate uid/gid ranges for this user on the host "+
				"(/etc/subuid, /etc/subgid) — the nested podman stays single-uid, so images that "+
				"switch user won't run; grant a range (usermod --add-subuids/--add-subgids) to fix")
		}
		return backend.NestedIDFiles{}
	}
	return backend.NestedIDFiles(t.base.NestedIDFiles(t.slug))
}

// runSetup runs the profile's one-time `setup:` script inside the sandbox before
// the user/agent takes over. It is gated by a per-sandbox stamp (the script's
// hash) so it runs once and re-runs only when the script changes. Setup runs
// under the same isolation and egress allowlist as the sandbox, so network
// installs need their domains allowed. A non-zero setup is fatal by default —
// we don't drop the caller into a half-prepared sandbox — and noSetup skips it.
func runSetup(t *target, rt config.Runtime, engine string, nestedIDs backend.NestedIDFiles, noSetup bool, errOut io.Writer) error {
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
	mountDest, srcMounts, mountGen, mountIDs, merr := t.mounts()
	if merr != nil {
		return merr
	}
	code, err := backendRun(backend.RunOpts{
		Engine: engine, Image: image, Spec: spec,
		Dest: t.base.SandboxDir(t.slug), Slug: t.slug,
		MountDest:       mountDest,
		MountGen:        mountGen,
		MountIDs:        mountIDs,
		SrcMounts:       srcMounts,
		HomeDir:         t.base.HomeDir(t.slug),
		DestGen:         t.base.Gen(t.slug),
		AuthEnv:         hostAuthEnv(t.profile),
		RT:              rt,
		Profile:         t.profile,
		ProfileJSONPath: t.base.ProfileJSONPath(t.slug),
		NestedIDFiles:   nestedIDs,
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
