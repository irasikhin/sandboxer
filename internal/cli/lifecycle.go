package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/backend"
	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/registry"
	"github.com/irasikhin/sandboxer/internal/sandbox"
	"github.com/irasikhin/sandboxer/internal/style"
)

// announceFreshState prints a one-time notice when this command initialised the
// project's runtime state tree, so the auto-created directory is never a
// surprise. The state lives outside the repo (config.StateDir).
func announceFreshState(w io.Writer, fresh bool, root string) {
	if fresh {
		style.Infof(w, "initialized state in %s", config.StateDir(root))
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
			announceFreshState(cmd.ErrOrStderr(), fresh, t.base.Src)
			if f.domains != "" {
				if err := t.base.SetDomains(f.domains); err != nil {
					return err
				}
			}
			if t.profile == nil {
				return fmt.Errorf("no profile for %q — scaffold one with 'sandboxer config init', then re-create", t.slug)
			}
			// Resolve and validate the WHOLE runtime before any state is
			// written: an invalid profile (a retired key, a bad proxy,
			// image.ref plus customization, an unknown backend, a bad session
			// mode) must fail while create has made NOTHING — running it after
			// the snapshot and worktrees left a half-created sandbox behind
			// with the diagnosis arriving over its corpse.
			rtCreate, err := t.runtime(f)
			if err != nil {
				return err
			}
			if err := config.ValidateBackend(rtCreate); err != nil {
				return err
			}
			if err := config.ValidateSession(rtCreate); err != nil {
				return err
			}
			if t.json != nil {
				if err := t.base.WriteProfileJSON(t.slug, t.json); err != nil {
					return err
				}
			}
			if err := t.base.MakeSandbox(t.slug, cmd.ErrOrStderr()); err != nil {
				return err
			}
			prepareHome(t, rtCreate, cmd.ErrOrStderr())
			warnIgnoredConfig(cmd.ErrOrStderr(), t.base.Src)
			fmt.Fprintln(cmd.ErrOrStderr(), style.Wrap(cmd.ErrOrStderr(), configLine(rtCreate, t.slug, t.profile, backendLabel(rtCreate)), style.Bold))
			// Same one-line-per-source report enter prints. create used to say
			// only where to review each branch, which left the one thing worth
			// knowing at create time — that a source was ADOPTED rather than
			// given its own worktree — off the screen entirely.
			for _, s := range t.base.Srcs(t.slug) {
				style.Infof(cmd.ErrOrStderr(), "src %s", srcLine(s))
			}
			warnOpenNetwork(cmd.ErrOrStderr(), rtCreate, t.profile)
			warnMicrovmProxy(cmd.ErrOrStderr(), rtCreate)
			reportPorts(cmd.ErrOrStderr(), rtCreate)
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
	fl.StringVar(&f.backend, "backend", "", "backend: microsandbox")
	fl.StringVar(&f.domains, "allow-domains", "", "egress allowlist (csv)")
	bindPorts(cmd, &f)
	return cmd
}

func newEnterCmd() *cobra.Command {
	var f commonFlags
	var sessionName string
	var quiet bool
	cmd := &cobra.Command{
		Use:   "enter [slug|profile|file.nix]",
		Short: "Open an interactive shell inside the sandbox",
		Long: `Open a shell inside the sandbox, attached to an in-sandbox tmux
session (detach: Ctrl-Space d; the session and anything running in it keep going
— a later enter drops straight back in, and a second terminal can attach the
same session in parallel). By default the shell runs in the persistent
session machine; --ephemeral runs a one-shot machine instead. A sandbox
that doesn't exist yet is created first.

The tmux server lives INSIDE the sandbox (tmux -L sandboxer, system
/etc/tmux.conf: mouse on, sandboxer prompt in every pane; the prefix is
Ctrl-Space, so Ctrl-Space c opens a new window). --session opens a separate
named session in the same machine.

When a replaced machine's saved layout is restored, panes that were running
a cataloged agent relaunch it with its resume command (claude --continue) —
disable with autoResume = false in the profile, or SANDBOXER_NO_RESUME=1.`,
		Example: `  # enter the active sandbox (see: sandboxer use)
  sandboxer enter

  # enter (or create) the sandbox "feat"
  sandboxer enter feat

  # a second terminal into the same tmux session
  sandboxer enter feat

  # a separate tmux session in the same machine
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
			// narrate carries the progress/banner chatter; --quiet drops it.
			// Warnings, errors and the interactive drift prompt stay on the
			// real stderr regardless.
			narrate := cmd.ErrOrStderr()
			if quiet {
				narrate = io.Discard
			}
			announceFreshState(narrate, fresh, t.base.Src)
			dest := t.base.SandboxDir(t.slug)
			createdDest := !fileExists(dest)
			if createdDest {
				style.Infof(narrate, "sandbox %q does not exist — creating", t.slug)
				if t.json != nil {
					if err := t.base.WriteProfileJSON(t.slug, t.json); err != nil {
						return err
					}
				}
				if err := t.base.MakeSandbox(t.slug, narrate); err != nil {
					return err
				}
			} else {
				if err := t.syncSnapshot(); err != nil {
					return err
				}
				// Converge the sources onto the (possibly edited) profile: new
				// srcs materialize under <slug>/ — a live session sees them
				// immediately through its stable mount.
				if _, err := t.base.SyncSrcs(t.slug, narrate); err != nil {
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
			fmt.Fprintln(narrate, style.Wrap(narrate, configLine(rt, t.slug, t.profile, backendLabel(rt)), style.Bold))
			// Show what the sandbox actually exposes — one line per source repo,
			// with its branch and where the worktree lives.
			for _, s := range t.base.Srcs(t.slug) {
				style.Infof(narrate, "src %s", srcLine(s))
			}
			warnOpenNetwork(errOut, rt, t.profile)
			warnMicrovmProxy(errOut, rt)
			reportPorts(errOut, rt)
			engine, err := backend.ResolveEngine(rt.Backend, config.LoadDefaults())
			if err != nil {
				return err
			}
			if err := t.base.EnsureHome(t.slug); err != nil {
				return err
			}
			prepareHome(t, rt, narrate)
			if err := runSetup(t, rt, engine, f.noSetup, narrate); err != nil {
				return err
			}
			name := backend.SessionName(t.slug, t.base.Dir)
			image, spec, err := resolveImage(t.profile, errOut)
			if err != nil {
				return err
			}
			mp, err := t.mounts()
			if err != nil {
				return err
			}
			o := backend.RunOpts{
				Engine: engine, Image: image, Spec: spec, Dest: dest, Slug: t.slug,
				MountDest:        mp.Dest,
				MountGen:         mp.Gen,
				MountIDs:         mp.IDs,
				SrcMounts:        mp.Src,
				GitMounts:        mp.Git,
				HomeDir:          t.base.HomeDir(t.slug),
				SessionStatePath: t.base.SessionStatePath(t.slug),
				DestGen:          t.base.Gen(t.slug),
				AuthEnv:          hostAuthEnv(t.profile),
				RT:               rt, Profile: t.profile,
				ProfileJSONPath: t.base.ProfileJSONPath(t.slug),
				Mem:             rt.Mem, CPU: rt.CPU,
				Interactive: true, Args: tmuxEnterArgs(sessionName),
				NoEgress: noEgress(),
				Stdin:    cmd.InOrStdin(), Stdout: cmd.OutOrStdout(), Stderr: errOut,
			}
			// Decide the session shape BEFORE announcing it. The banner
			// promises detach semantics, and announcing first told the user
			// "exiting keeps the machine running" and then handed them a
			// one-shot machine, where detaching tmux exits the machine's
			// main process and takes the whole session with it.
			useSession := false
			staleWhy := "" // non-empty: attach to the running stale session as-is
			oneShotWhy := ""
			if persistent {
				o.BaseDir = t.base.Dir
				// A session running with a stale config (profile changed or image
				// rebuilt) is never torn down silently — but it is not sidestepped
				// into a one-shot machine either. That fallback handed the user a
				// one-shot machine where Ctrl-Space d destroys everything, left
				// the real session unreachable, and — because nothing ever converged
				// it — did so again on EVERY later enter: a permanent one-shot trap.
				// So ask the session what it is holding: nothing (SessionIdle) means
				// there is nothing to lose, so converge it; a live tmux session holds
				// the user's agent and is attached as-is, with the pending config
				// called out. Either way enter lands in the session machine, so
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
						staleWhy, driftDetail, drift = mountDriftWhy(o, info, mp.IDs)
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
						fmt.Fprintln(errOut, style.Wrap(errOut, driftDetail, style.Yellow))
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
				fmt.Fprintln(narrate, style.Banner(narrate, staleSessionEnterBanner(t.slug, engine, dest, name, staleWhy)))
			case useSession:
				fmt.Fprintln(narrate, style.Banner(narrate, persistentEnterBanner(t.slug, engine, dest, name)))
			default:
				fmt.Fprintln(narrate, style.Banner(narrate, oneShotEnterBanner(t.slug, engine, dest, oneShotWhy)))
			}
			var code int
			var runErr error
			attached := false
			switch {
			case staleWhy != "":
				// Attach to what is already running — deliberately NOT through
				// EnsureSession, which would recreate the stale machine.
				verifyPorts(errOut, rt, t.slug, engine, name)
				code, runErr = backendExecSession(o, name, tmuxEnterArgs(sessionName))
				attached = true
			case useSession:
				if _, err := backendEnsureSession(o); err != nil {
					// EnsureSession's message IS the diagnostic (busy session, egress
					// failure, …) — print it here; the tail then returns silently.
					style.Errorf(errOut, "%v", err)
					runErr = err
				} else {
					verifyPorts(errOut, rt, t.slug, engine, name)
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
				// machine death (host reboot, engine restart) — and a
				// deliberate full exit resets it, keeping the exit banner's
				// "the next enter opens a fresh one" true.
				backendSyncSessionState(engine, name, o.SessionStatePath)
			}
			for _, s := range t.base.Srcs(t.slug) {
				style.Infof(narrate, "%s: work is in %s — commit/review on the host: git -C %s log %s",
					filepath.Base(s.RepoRoot), s.Path, s.RepoRoot, s.Branch)
			}
			style.Infof(narrate, "done in %s", dest)
			if runErr != nil {
				return silentErr{runErr}
			}
			if code != 0 {
				return exitErr{code}
			}
			return nil
		},
	}
	bindExisting(cmd, &f)
	cmd.Flags().BoolVar(&f.noSetup, "no-setup", false, "skip the profile's one-time setup script")
	cmd.Flags().BoolVar(&f.ephemeral, "ephemeral", false, "one-shot machine instead of the persistent session")
	cmd.Flags().BoolVar(&f.recreate, "recreate", false, "force session rebuild even if running (picks up config changes)")
	cmd.Flags().StringVar(&sessionName, "session", "main", "tmux session name inside the sandbox")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "suppress the progress narration (warnings and errors still print)")
	return cmd
}

func newExecCmd() *cobra.Command {
	var f commonFlags
	var quiet bool
	cmd := &cobra.Command{
		Use:   "exec [slug] -- <cmd...>",
		Short: "Run a command inside the sandbox",
		Long: `Run a command inside the sandbox. The command's exit code becomes
sandboxer's own exit code — a failing test run fails the exec too, so it
composes with scripts and CI.`,
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
			// Same narration split as enter: --quiet drops the chatter,
			// warnings and errors keep the real stderr.
			narrate := cmd.ErrOrStderr()
			if quiet {
				narrate = io.Discard
			}
			dest := t.base.SandboxDir(t.slug)
			if !fileExists(dest) {
				return fmt.Errorf("no sandbox %q (create it: sandboxer create)", t.slug)
			}
			if err := t.syncSnapshot(); err != nil {
				return err
			}
			if _, err := t.base.SyncSrcs(t.slug, narrate); err != nil {
				return err
			}
			// The nested podman's docker-compatible API socket is the one thing
			// an exec (unlike an interactive shell, whose rc ensures it) would
			// otherwise start without — testcontainers and docker clients would
			// fail with "cannot connect to the daemon". Ensure it lazily in the
			// same wrap both the session and the one-shot path run.
			rest = podmanSocketPrefix(rest)
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
			fmt.Fprintln(narrate, style.Wrap(narrate, configLine(rt, t.slug, t.profile, backendLabel(rt)), style.Bold))
			warnOpenNetwork(cmd.ErrOrStderr(), rt, t.profile)
			warnMicrovmProxy(cmd.ErrOrStderr(), rt)
			reportPorts(cmd.ErrOrStderr(), rt)
			engine, err := backend.ResolveEngine(rt.Backend, config.LoadDefaults())
			if err != nil {
				return err
			}
			if err := t.base.EnsureHome(t.slug); err != nil {
				return err
			}
			prepareHome(t, rt, narrate)
			if err := runSetup(t, rt, engine, f.noSetup, narrate); err != nil {
				return err
			}
			image, spec, err := resolveImage(t.profile, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			mp, err := t.mounts()
			if err != nil {
				return err
			}
			o := backend.RunOpts{
				Engine: engine, Image: image, Spec: spec, Dest: dest, Slug: t.slug,
				MountDest: mp.Dest,
				MountGen:  mp.Gen,
				MountIDs:  mp.IDs,
				SrcMounts: mp.Src,
				GitMounts: mp.Git,
				HomeDir:   t.base.HomeDir(t.slug),
				DestGen:   t.base.Gen(t.slug),
				AuthEnv:   hostAuthEnv(t.profile),
				RT:        rt, Profile: t.profile,
				ProfileJSONPath: t.base.ProfileJSONPath(t.slug),
				Mem:             rt.Mem, CPU: rt.CPU,
				Interactive: true, Args: rest,
				NoEgress: noEgress(),
				Stdin:    cmd.InOrStdin(), Stdout: cmd.OutOrStdout(), Stderr: cmd.ErrOrStderr(),
			}
			// exec rides an existing running+fresh session but NEVER creates or
			// replaces the daemon machine — that is enter's job. Anything else
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
						why, detail, drift := mountDriftWhy(o, info, mp.IDs)
						if drift {
							fmt.Fprintln(cmd.ErrOrStderr(), style.Wrap(cmd.ErrOrStderr(), detail, style.Yellow))
						}
						staleExecNotice(cmd.ErrOrStderr(), name, why, t.slug)
					case !backend.ImageFresh(info.ImageID, backendImageID(engine, o.Image)):
						staleExecNotice(cmd.ErrOrStderr(), name, "image rebuilt", t.slug)
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
				// Pass the command's exit code through (scripts and CI depend
				// on telling "the tests failed" from "sandboxer failed").
				return exitErr{code}
			}
			return nil
		},
	}
	bindExisting(cmd, &f)
	cmd.Flags().BoolVar(&f.noSetup, "no-setup", false, "skip the profile's one-time setup script")
	cmd.Flags().BoolVar(&f.ephemeral, "ephemeral", false, "one-shot machine instead of the persistent session")
	cmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "suppress the progress narration (warnings and errors still print)")
	return cmd
}

// prepareHome readies the sandbox's private home before anything runs in the
// sandbox (after EnsureHome), in the order the two steps require:
//
//  1. the host's agent configs are seeded when the profile opts in
//     (hostConfigs = true) — copy-only, never-overwrite semantics live in
//     sandbox.SeedHome; this is just the profile gate;
//  2. the image's baked-in pi packages are registered in pi's settings
//     (sandbox.EnsurePiPackages), which MERGES into the settings.json step 1
//     may just have seeded — hence second: registering first would leave the
//     seed's never-overwrite rule to skip the host's own pi settings.
func prepareHome(t *target, rt config.Runtime, w io.Writer) {
	if t.profile != nil && t.profile.HostConfigs {
		t.base.SeedHome(t.slug, w)
	}
	if rt.PiPackages {
		t.base.EnsurePiPackages(t.slug, w)
	}
}

// hostAuthEnv collects the registry agents' auth env vars that are set (and
// non-empty) in the HOST environment — CLAUDE_CODE_OAUTH_TOKEN, API keys — for
// the guest env, gated by the same hostConfigs opt-in as the config seed.
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

// noGit reports whether the sources' opt-in git-dir shares are disabled via
// the environment — the operator kill-switch that forces every source back to
// the default (no git inside), whatever the profile says.
func noGit() bool { return os.Getenv("SANDBOXER_NO_GIT") == "1" }

// warnMicrovmProxy notes the egress posture when a sandbox has BOTH a proxy
// and an allowlist: the proxy is the egress path (open network at the VM
// layer), and the allowlist is then enforced by the proxy rather than by the
// runner's network rules — reaching the proxy needs the open network, which
// cannot also filter at the network layer. The user should ensure their proxy
// enforces the intended allowlist. Not an error: a proxy + a default allowlist
// is the common case.
func warnMicrovmProxy(w io.Writer, rt config.Runtime) {
	if rt.Proxy == "" || !rt.Egress {
		return
	}
	style.Warnf(w, "egress.proxy is set — direct traffic is walled by the VM allowlist, and "+
		"the proxy is reachable on its one port. Traffic that RIDES the proxy is constrained by the "+
		"proxy itself, not the VM — make sure it restricts egress to what you intend.")
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
	msg := "WARNING — egress is unrestricted (no allowlist, no proxy); the agent can reach any host"
	if prof != nil && prof.HostConfigs {
		msg += " — and hostConfigs is on, so seeded credentials could be exfiltrated"
	}
	style.Warnf(w, "%s", msg)
}

// reportPorts names every published forward, so the URL to open is on screen
// instead of being reconstructed from the config — and flags the one that is
// not merely local: a non-loopback bind puts a guest port on the network, where
// the sandbox's isolation stops being about this machine only.
func reportPorts(w io.Writer, rt config.Runtime) {
	for _, p := range rt.Ports {
		style.Infof(w, "port %s (open http://%s:%d/ once something listens on %d inside)",
			p, p.Bind, p.Host, p.Guest)
		if p.Public() {
			style.Warnf(w, "WARNING — %s:%d is published on a NON-loopback address; "+
				"anyone who can reach this host can reach the sandbox's port %d", p.Bind, p.Host, p.Guest)
		}
	}
}

// verifyPorts checks the forwards against the machine that is about to be
// attached — the runner's own view, not the resolved config. enter advertises
// the ports it resolved ("open http://127.0.0.1:3080/"), but a forward lives in
// the CREATE argv: a session created before the port was configured does not
// have it, and enter deliberately attaches to a running session rather than
// rebuilding it under a live tmux. Without this the two messages contradict
// each other and the browser gets the last word ("unable to connect") on a URL
// sandboxer had just printed. Silent when the runner cannot answer — an unknown
// is not evidence of a missing forward.
func verifyPorts(w io.Writer, rt config.Runtime, slug, engine, machine string) {
	if len(rt.Ports) == 0 {
		return
	}
	live := backendSessionPorts(engine, machine)
	if live == nil {
		return
	}
	var missing []string
	for _, p := range rt.Ports {
		if !slices.Contains(live, p) {
			missing = append(missing, p.String())
		}
	}
	if len(missing) == 0 {
		return
	}
	style.Warnf(w, "WARNING — this machine does NOT publish %s: a forward is part of the create argv, "+
		"so nothing listens on the host until the session is rebuilt (sandboxer stop %s && sandboxer enter %s)",
		strings.Join(missing, ", "), slug, slug)
}

// podmanSocketPrefix wraps an in-guest user command so the nested podman's
// docker-compatible API socket — the docker.sock testcontainers and docker
// clients connect to — is ensured before the command runs (the interactive
// shell path gets the same via rc.sh). The ensure is idempotent and
// NON-fatal (`|| true`): a sandbox whose socket cannot come up still runs
// the command, and the failure surfaces where it belongs, in the tool that
// needs the socket. The wrap re-execs the original command with its argv
// intact: $0 carries the original argv0 through bash -c, and exec replaces
// the wrapper process, so exit codes and signals propagate unchanged.
func podmanSocketPrefix(cmd []string) []string {
	if len(cmd) == 0 {
		return nil
	}
	return append([]string{"bash", "-c",
		"command -v podman-socket >/dev/null 2>&1 && podman-socket >/dev/null 2>&1 || true; exec \"$0\" \"$@\"",
		cmd[0]}, cmd[1:]...)
}

// tmuxLaunch wraps an in-guest launch command (the then-branch) with the
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

// tmuxEnterArgs is the in-guest command for `enter`: attach to (or create)
// the named session on the in-guest `tmux -L sandboxer` server.
func tmuxEnterArgs(session string) []string {
	return tmuxLaunch("exec tmux -L sandboxer new-session -A -s " + session)
}

// tmuxRestoreArgs is tmuxEnterArgs plus a one-time layout restore: when a saved
// session state exists at statePath (written before this machine replaced the
// last one), the attach first rebuilds the recorded windows/panes and then
// attaches. The rebuild is idempotent — a live session is left untouched by the
// `has-session` guard baked into the script — so with no saved state it is
// exactly tmuxEnterArgs, and callers use it unconditionally. The saved state is
// NOT consumed here (it is deleted only by rm/clean): a fresh machine has no
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

// persistentEnterBanner names the session machine and separates the two
// ways out, because they are NOT equivalent and conflating them cost a user
// their session: detaching leaves the tmux session running, while exiting the
// shell closes its last pane — which ends the tmux session, and with tmux's
// default exit-empty the whole in-guest server. The MACHINE survives
// either way, which is what the old one-liner said, and that half-truth read
// as "exiting is safe too". Only printed once the session machine is
// actually the thing about to run (see enter's useSession).
func persistentEnterBanner(slug, engine, dir, machine string) string {
	return fmt.Sprintf(
		"sandboxer: persistent session %s in %q (%s) — %s\n"+
			"sandboxer: Ctrl-Space d DETACHES (also Ctrl-b d, Alt-d, or type `detach`) — the tmux session and everything in it keep running; reattach: sandboxer enter %s\n"+
			"sandboxer: exiting the shell ENDS that tmux session (the machine itself stays) — the next enter opens a fresh one",
		machine, slug, engine, dir, slug)
}

// staleSessionEnterBanner covers the one case where enter attaches to a
// session it cannot converge: the machine is running with a stale config but
// holds a live tmux session, so rebuilding it would kill whatever is in there.
// Detach semantics are the persistent ones (that is the whole point — the user
// lands in their real session), and the pending config is stated with the one
// command that applies it, so "my change did nothing" is never a mystery.
func staleSessionEnterBanner(slug, engine, dir, machine, why string) string {
	return fmt.Sprintf(
		"sandboxer: persistent session %s in %q (%s) — %s\n"+
			"sandboxer: attaching as-is: the session is stale (%s) but is running a tmux session — not rebuilding it under you\n"+
			"sandboxer: the new configuration applies once it is empty, or now: sandboxer stop %s && sandboxer enter %s\n"+
			"sandboxer: Ctrl-Space d DETACHES (also Ctrl-b d, Alt-d, or type `detach`) — the tmux session and everything in it keep running; reattach: sandboxer enter %s\n"+
			"sandboxer: exiting the shell ENDS that tmux session (the machine itself stays) — the next enter opens a fresh one",
		machine, slug, engine, dir, why, slug, slug, slug)
}

// staleExecNotice explains exec's one-shot fallback for a stale session. A
// single command in a throwaway machine costs nothing and runs with the
// configuration just asked for, so the fallback is right here — but the old
// wording ("re-enter to refresh it") promised more than enter delivers: a
// session still holding a tmux session is attached, not rebuilt. Name both
// ways it actually refreshes.
func staleExecNotice(w io.Writer, machine, why, slug string) {
	style.Warnf(w, "session %s is stale (%s) — running one-shot; it refreshes on the next enter"+
		" once that session is empty, or now: sandboxer stop %s && sandboxer enter %s",
		machine, why, slug, slug)
}

// oneShotEnterBanner is the honest banner for an ephemeral machine: there,
// tmux is the machine's MAIN workload, so detaching (Ctrl-Space d) exits it
// and the machine — and the session — go with it. That is the opposite of
// what the persistent banner promises, which is why the two are chosen only
// after the mode is decided. why names which ephemeral switch is in force —
// the persistent path never lands here: a stale session is converged or
// attached, never sidestepped into a machine detach would destroy.
func oneShotEnterBanner(slug, engine, dir, why string) string {
	b := fmt.Sprintf("sandboxer: one-shot machine in %q (%s) — %s\n", slug, engine, dir)
	if why != "" {
		b += "sandboxer: " + why + "\n"
	}
	return b + "sandboxer: this machine is one-shot: Ctrl-Space d or exiting ENDS it and everything running in it" +
		" (committed and uncommitted work in the source worktrees is on the host and survives)"
}

// ephemeralWhy names WHICH of the three ephemeral switches is in force, in the
// precedence order config.ResolveRuntime applies (flag, then the env
// kill-switch, then the profile). Without it a one-shot machine is a silent
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

// backendRun is the one-shot-run seam, overridable in tests so the setup
// orchestration (gate → run → stamp) can be exercised without a real engine.
var backendRun = backend.Run

// Session seams beside backendRun, for the same reason: the persistent
// enter/exec orchestration is exercised in tests without a real engine.
var (
	backendEnsureSession    = backend.EnsureSession
	backendExecSession      = backend.ExecSession
	backendInspectSession   = backend.InspectSession
	backendSessionPorts     = backend.SessionPorts
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
		style.Infof(errOut, "skipping setup for %q (--no-setup)", t.slug)
		return nil
	}
	image, spec, rerr := resolveImage(t.profile, errOut)
	if rerr != nil {
		return rerr
	}
	style.Infof(errOut, "running setup for %q…", t.slug)
	mp, merr := t.mounts()
	if merr != nil {
		return merr
	}
	// The script's output goes to the terminal AND to _logs/<slug>.setup.log,
	// so a failure that scrolled away stays debuggable (troubleshooting.md
	// points there). Best-effort: no log file never blocks the setup itself.
	setupOut := errOut
	logPath := ""
	if f, ferr := os.Create(t.base.LogPath(t.slug, "setup.log")); ferr == nil {
		defer f.Close()
		logPath = f.Name()
		setupOut = io.MultiWriter(errOut, f)
	}
	code, err := backendRun(backend.RunOpts{
		Engine: engine, Image: image, Spec: spec,
		Dest: t.base.SandboxDir(t.slug), Slug: t.slug,
		MountDest:       mp.Dest,
		MountGen:        mp.Gen,
		MountIDs:        mp.IDs,
		SrcMounts:       mp.Src,
		GitMounts:       mp.Git,
		HomeDir:         t.base.HomeDir(t.slug),
		DestGen:         t.base.Gen(t.slug),
		AuthEnv:         hostAuthEnv(t.profile),
		RT:              rt,
		Profile:         t.profile,
		ProfileJSONPath: t.base.ProfileJSONPath(t.slug),
		Mem:             rt.Mem,
		CPU:             rt.CPU,
		Interactive:     false,
		Args:            podmanSocketPrefix([]string{"bash", "-lc", t.profile.Setup}),
		NoEgress:        noEgress(),
		Stdout:          setupOut,
		Stderr:          setupOut,
	})
	if err != nil {
		return fmt.Errorf("setup failed to start: %w", err)
	}
	if code != 0 {
		hint := "fix the `setup:` script or re-run with --no-setup"
		if logPath != "" {
			hint += " (output saved: " + logPath + ")"
		}
		return fmt.Errorf("setup exited %d — %s", code, hint)
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
