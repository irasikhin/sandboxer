package backend

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/egress"
	"github.com/irasikhin/sandboxer/internal/execx"
	"github.com/irasikhin/sandboxer/internal/toolbox"
)

// ContainerProxyURL adapts a configured proxy URL for use from inside a
// container. The rewrite itself lives in config so the sandbox container, the
// egress sidecar and the image builder all share one implementation (toolbox
// cannot import backend — backend imports toolbox for the auto-build); this
// stays as the name backend's own call sites read naturally.
func ContainerProxyURL(raw string) string { return config.ContainerProxyURL(raw) }

// containerRoutes adapts a profile's proxy routes for use from inside the
// container: each route's proxy gets the same localhost→host-gateway rewrite as
// the default proxy (ContainerProxyURL), so a route pointing at a proxy on the
// host resolves from the egress sidecar. nil in, nil out.
func containerRoutes(routes []config.Route) []config.Route {
	if len(routes) == 0 {
		return nil
	}
	out := make([]config.Route, len(routes))
	for i, r := range routes {
		out[i] = config.Route{Domains: r.Domains, Proxy: ContainerProxyURL(r.Proxy)}
	}
	return out
}

// RunOpts configures a single container invocation.
type RunOpts struct {
	Engine string
	Image  string
	Spec   toolbox.Spec // image variant customization; drives the auto-build of a missing variant
	Dest   string       // sandbox root (<slug>/), the workdir; mounted rw only when MountDest
	// MountDest bind-mounts Dest itself rw — the whole sandbox root as one
	// stable window (a srcs edit shows up in a running session without
	// recreating it). It is set for a sandbox no source narrows.
	//
	// A narrowed sandbox clears it, and that is the CONTAINMENT BOUNDARY: the
	// worktrees under Dest are complete on the host, so mounting Dest would
	// hand the container every excluded file. Unmounted, they are unreachable —
	// what is not in SrcMounts does not exist inside. Never set this because a
	// mount seems to be missing; the false is what makes narrowing real. See
	// sandbox.Mounts, which decides both fields together.
	MountDest bool
	// SrcMounts are the source directories bind-mounted rw at their own host
	// paths: the adopted worktrees when MountDest (they live outside Dest), else
	// every source's exposed directories. Sorted by the caller: the order is
	// part of the ConfigHash contract.
	SrcMounts []string
	Slug      string
	BaseDir   string // host state dir (config.StateDir); names/labels the persistent session (zero value fine for one-shot runs)
	HomeDir   string // sandbox-private agent home, mounted as $HOME (isolated per sandbox)
	// DestGen is the sandbox directory's generation (sandbox.Base.Gen) — bumped
	// whenever the dir at Dest had to be created from nothing. It travels as a
	// container env var, which folds it into the session ConfigHash: a session
	// created before a hand-deleted-and-recreated tree still bind-mounts the
	// DELETED directory, and the generation flip is what makes it read as stale
	// instead of silently reused. "" (a pre-gen sandbox) adds no flag, keeping
	// existing sessions' hashes unchanged.
	DestGen string
	// MountGen fingerprints the on-disk identity (device+inode) of the
	// individual source mounts in SrcMounts — the view directories of a narrowed
	// sandbox, and any adopted worktrees. Like DestGen it travels as a container
	// env var folded into the session ConfigHash, and for the same reason: a
	// bind mount is pinned to the inode it names, so a host-side git operation
	// (checkout, rebase) that removes and recreates a mounted directory leaves a
	// live session bound to the orphaned OLD inode — the agent reading stale
	// files and writing where nobody looks. When the fingerprint changes the
	// session hash flips, so the next enter/exec rebuilds against the fresh
	// directories. Empty for a sandbox with no individual mounts (the common
	// case: one managed source, no include, whose <slug>/ root mount is itself
	// inode-stable), keeping that argv — and its session hash — unchanged.
	MountGen string
	// MountIDs is the same identity material MountGen hashes, encoded for the
	// sandboxer.mounts label (sandbox.EncodeMountIDs). MountGen makes a moved
	// mount set read as stale; this makes it EXPLAINABLE — diffed against a
	// fresh resolve it names which directory appeared, which one the host
	// recreated under a live session's feet, and which one is gone, instead of
	// reporting every drift as the "profile changed" it usually is not.
	//
	// Deliberately absent from commonArgs, and therefore from ConfigHash: it is
	// stamped as a LABEL only (createArgv), and labels are excluded from the
	// hash by construction. MountGen already carries this identity into the
	// hash — putting the same material in twice would double-count it and, worse,
	// flip the hash of every session that exists today the moment this field
	// ships. TestConfigHash's "mount IDs" case pins that.
	MountIDs string
	// AuthEnv is the agents' auth environment ("KEY=value" entries, sorted by
	// the caller), collected by the CLI from the HOST environment when the
	// profile opts into hostConfigs: long-lived tokens like
	// CLAUDE_CODE_OAUTH_TOKEN (`claude setup-token`) or plain API keys. Env is
	// the sanctioned channel for these — unlike a copied OAuth credentials
	// FILE, whose rotating refresh chain dies (or hijacks the host's session)
	// on the next refresh either side performs. The profile's own env is
	// appended after it, so it still overrides per key.
	//
	// It is set on the PROCESS, never on the session container: `run` bakes it
	// (the agent is that container's main process) while a session shell gets
	// it per `exec`. Two reasons. It keeps credentials out of the long-lived
	// container's inspectable environment; and, decisively, it keeps them out
	// of ConfigHash — which fingerprints the create argv. A hash that moved
	// with a token value made every rotation, and every terminal that happened
	// not to export the var, read as "profile changed", so a session went
	// permanently stale from ambient shell state rather than from the config.
	// Each new shell picks up the current value with no rebuild at all.
	AuthEnv         []string
	RT              config.Runtime
	Profile         *config.Profile
	ProfileJSONPath string // mounted ro at /run/sandboxer/profile.json if present
	Interactive     bool
	NoEgress        bool   // SANDBOXER_NO_EGRESS
	Mem             string // memory cap → --memory (e.g. 2G); empty = unlimited
	CPU             string // CPU cap → --cpus (accepts a float or systemd "100%")
	Pids            int    // PID-count cap → --pids-limit; 0 = unlimited
	Args            []string
	Stdin           io.Reader
	Stdout          io.Writer
	Stderr          io.Writer
}

// Run executes the container, wiring isolation, credentials, dependency mounts
// and (when applicable) the egress allowlist. It returns the container exit
// code.
func Run(o RunOpts) (int, error) {
	// Make sure the toolbox image is present before anything else. The bundled
	// default is never published, so a missing one is auto-built instead of
	// letting the engine attempt a doomed pull and drop the user back to the
	// host shell.
	if err := ensureImage(o); err != nil {
		return 0, err
	}

	var eg *egress.Egress
	// Egress allowlist is required unless explicitly disabled (NoEgress /
	// egress.enabled = false) or an upstream proxy is holding the boundary. When it is
	// required we fail closed: we never silently fall back to an open bridge
	// network, because that would drop the isolation the caller asked for.
	if egressRequired(o) {
		if len(o.RT.Domains) == 0 {
			return 0, errEmptyAllowlist
		}
		if err := ensureProxyImage(o); err != nil {
			return 0, err
		}
		e, err := egress.Up(o.Engine, o.Slug, o.RT.Domains, ContainerProxyURL(o.RT.Proxy), containerRoutes(o.RT.Routes), o.BaseDir, o.Stderr)
		if err != nil {
			return 0, fmt.Errorf("egress allowlist proxy failed to start: %w — "+
				"refusing to run on an open network (disable with egress.enabled = false or SANDBOXER_NO_EGRESS=1)", err)
		}
		eg = e
	}
	defer eg.Down()

	egNet, egProxyURL := "", ""
	if eg.Active() {
		egNet, egProxyURL = eg.Net(), eg.ProxyURL()
	}
	args, err := runArgs(o, egNet, egProxyURL)
	if err != nil {
		return 0, err
	}

	cmd := exec.Command(o.Engine, args...)
	cmd.Stdin = o.Stdin
	cmd.Stdout = o.Stdout
	cmd.Stderr = o.Stderr
	return exitCode(cmd.Run()), nil
}

// RunArgv returns the engine `run` argv that Run would execute for o, without an
// active egress sidecar (the allowlist proxy is created dynamically at run time).
// It is the seam used by `sandboxer compose` to show/emit the run configuration.
func RunArgv(o RunOpts) ([]string, error) { return runArgs(o, "", "") }

// runArgs assembles the engine `run` argv. egNet/egProxyURL carry the egress
// sidecar's network name and proxy URL when the allowlist proxy is active (both
// "" otherwise). Kept identical to the original inline construction so Run's
// behavior is unchanged.
func runArgs(o RunOpts, egNet, egProxyURL string) ([]string, error) {
	args := []string{"run", "--rm"}
	if o.Interactive {
		args = append(args, "-i")
		// -t only with a real TTY (docker errors "not a TTY" in pipes/CI).
		if IsTerminal(o.Stdin) && IsTerminal(o.Stdout) {
			args = append(args, "-t")
		}
	}
	// Auth env belongs to the PROCESS, not the container (see RunOpts.AuthEnv):
	// here the agent IS the container's main process, so it is set at run time.
	// Before commonArgs so the profile's own env — appended at its tail — still
	// wins per key, exactly as when this lived inside commonArgs.
	args = append(args, authEnvArgs(o)...)
	args = append(args, commonArgs(o, egNet, egProxyURL)...)
	args = append(args, o.Image)
	args = append(args, o.Args...)
	return args, nil
}

// authEnvArgs renders the agents' auth environment as engine --env flags.
func authEnvArgs(o RunOpts) []string {
	args := make([]string, 0, 2*len(o.AuthEnv))
	for _, kv := range o.AuthEnv {
		args = append(args, "--env", kv)
	}
	return args
}

// commonArgs assembles every engine flag shared by the one-shot `run` and the
// persistent-session `create` paths: everything between the run-mode prefix
// (`run --rm [-i] [-t]` vs `run -d --init --name … --label …`) and the image —
// isolation, workdir + sandbox mounts, identity env, host-gateway aliases,
// resource limits, proxy/egress env, credentials and the profile's extra
// mounts/env. The flag order is part of the contract: ConfigHash fingerprints
// this argv, so reordering a flag invalidates every existing session.
func commonArgs(o RunOpts, egNet, egProxyURL string) []string {
	args := containerUserArgs()
	args = append(args,
		"--cap-drop=ALL", "--security-opt", "no-new-privileges",
		"--workdir", o.Dest)
	// A narrowed sandbox deliberately leaves Dest unmounted — see
	// RunOpts.MountDest. The engine then materializes it (and the parents of
	// each source mount) as an empty directory in the container's own layer,
	// owned by root: with --user set, a write outside the exposed directories
	// fails loudly instead of vanishing with the container.
	if o.MountDest {
		args = append(args, "--volume", o.Dest+":"+o.Dest+":rw")
	}
	args = append(args,
		"--env", "SANDBOXER_IN_CONTAINER=1",
		"--env", "SANDBOXER_SLUG="+o.Slug, "--env", "SANDBOXER_SANDBOX_DIR="+o.Dest,
		// A UTF-8 locale by default: the image is locale-less, and without one
		// tmux downgrades every non-ASCII glyph to '_' (agent TUIs turn to
		// underscores on reattach). The profile's env is appended later and
		// the last --env occurrence wins, so a user LANG overrides this.
		"--env", "LANG=C.UTF-8",
	)
	// The sandbox dir's generation (see RunOpts.DestGen): flag only when set,
	// so a pre-gen sandbox keeps its argv — and session hash — byte-identical.
	if o.DestGen != "" {
		args = append(args, "--env", "SANDBOXER_SANDBOX_GEN="+o.DestGen)
	}
	// The source mounts' inode fingerprint (see RunOpts.MountGen): flag only
	// when set — a sandbox with no individual mounts keeps its argv, and hash,
	// byte-identical. This must sit right after the gen flag and before any
	// other conditional flag so the emitted order is deterministic.
	if o.MountGen != "" {
		args = append(args, "--env", "SANDBOXER_MOUNT_GEN="+o.MountGen)
	}
	// NO auth env here: it is scoped to the process that needs it (runArgs for
	// a one-shot, execArgv for a session shell), never baked into the session
	// container. See RunOpts.AuthEnv for why.
	//
	// $HOME is the sandbox-private agent home, bound at its own host path. It is
	// isolated per sandbox (see sandbox.Base.HomeDir): the host's real home is
	// never mounted, so no host config leaks in and the agent's atomic config
	// rewrites (e.g. ~/.claude.json) land on a real directory rather than a
	// bind-mounted file. The image has no /home, so there is nothing to shadow.
	if o.HomeDir != "" {
		args = append(args, "--env", "HOME="+o.HomeDir,
			"--volume", o.HomeDir+":"+o.HomeDir+":rw")
	}
	// The source mounts (see RunOpts.SrcMounts): adopted worktrees, or every
	// source's exposed directories when the sandbox is narrowed.
	// GIT METADATA IS NEVER MOUNTED: a worktree's .git file points at a host
	// path that does not exist in the container, so git inside fails cleanly —
	// the mounted directories ARE the access boundary, commits happen on the
	// host, and the whole git-RCE surface (hooks, config, filters) is gone with
	// the mount. See SECURITY.md.
	for _, m := range o.SrcMounts {
		args = append(args, "--volume", m+":"+m+":rw")
	}
	if o.Engine == "podman" {
		args = append(args, "--userns=keep-id")
	}
	args = append(args, nestedContainerArgs(o.Profile)...)
	// Both engines' host-gateway aliases, so a host-running service (e.g. a
	// user's own proxy) is reachable from inside the container regardless of
	// engine — see config.HostGatewayArgs.
	args = append(args, config.HostGatewayArgs()...)
	// Resource limits (the banner advertises these on every backend).
	if o.Mem != "" {
		args = append(args, "--memory", o.Mem)
	}
	if cpus := cpusFromQuota(o.CPU); cpus != "" {
		args = append(args, "--cpus", cpus)
	}
	if o.Pids > 0 {
		args = append(args, "--pids-limit", strconv.Itoa(o.Pids))
	}
	// Proxy wiring. Two modes, decided by whether the egress sidecar is active:
	//   - egNet != "": allowlist on. The agent's sole exit is the squid sidecar,
	//     so HTTP(S)_PROXY point at it; when a proxy URL is configured the sidecar
	//     itself chains to it (squid cache_peer, set in egress.Up).
	//   - egNet == "" with a proxy URL: allowlist off (direct mode). The agent
	//     talks to the proxy directly; NO_PROXY carries the configured bypass list.
	if egNet != "" {
		args = append(args, "--network", egNet,
			"--env", "HTTP_PROXY="+egProxyURL, "--env", "http_proxy="+egProxyURL,
			"--env", "HTTPS_PROXY="+egProxyURL, "--env", "https_proxy="+egProxyURL,
			"--env", "NO_PROXY=localhost,127.0.0.1", "--env", "no_proxy=localhost,127.0.0.1")
	} else if p := ContainerProxyURL(o.RT.Proxy); p != "" {
		args = append(args, "--env", "HTTP_PROXY="+p, "--env", "http_proxy="+p,
			"--env", "HTTPS_PROXY="+p, "--env", "https_proxy="+p)
		if o.RT.NoProxy != "" {
			args = append(args, "--env", "NO_PROXY="+o.RT.NoProxy, "--env", "no_proxy="+o.RT.NoProxy)
		}
	}
	if o.ProfileJSONPath != "" && pathExists(o.ProfileJSONPath) {
		args = append(args, "--volume", o.ProfileJSONPath+":/run/sandboxer/profile.json:ro")
	}
	if csv := o.RT.DomainsCSV(); csv != "" {
		args = append(args, "--env", "SANDBOXER_ALLOW_DOMAINS="+csv)
	}
	args = append(args, extraMountsAndEnv(o.Profile)...)
	return args
}

// nestedContainerArgs relaxes exactly what a ROOTLESS podman inside the sandbox
// needs, and nothing more. It is empty unless the profile set nestedContainers,
// so every other profile's argv — and therefore every existing session's
// ConfigHash — stays byte-identical to before this knob existed.
//
// The image has shipped podman for a while; what stopped it was the OUTER
// container's own sandboxing:
//
//   - seccomp: the engine's default profile denies clone(CLONE_NEWUSER) to a
//     process without CAP_SYS_ADMIN, and podman re-execs itself into a user
//     namespace ("cannot clone: Operation not permitted").
//   - masked /proc: the engine overmounts paths under /proc, after which the
//     kernel refuses the fresh procfs the nested container mounts
//     ("crun: mount `proc` to `proc`: Operation not permitted").
//   - /dev/net/tun: pasta, podman's rootless network backend, opens it.
//   - /dev/fuse: fuse-overlayfs, the nested podman's storage driver (see the
//     image's storage.conf), mounts layers through it.
//
// What it deliberately does NOT do: --privileged, --cap-add, or relaxing
// no-new-privileges. Capabilities stay dropped, which leaves the nested podman
// without a subordinate uid range — single-uid mapping, which the image's
// ignore_chown_errors is there to absorb. The trade is real and documented in
// SECURITY.md: a sandbox with nestedContainers has no syscall filter.
func nestedContainerArgs(p *config.Profile) []string {
	if p == nil || !p.NestedContainers {
		return nil
	}
	return []string{
		"--security-opt", "seccomp=unconfined",
		"--security-opt", "systempaths=unconfined",
		"--device", "/dev/net/tun",
		"--device", "/dev/fuse",
	}
}

// containerUserArgs is the `--user` flag for the sandbox container. By default
// it maps to the invoking host uid:gid so bind-mounted files (worktree, $HOME,
// git objects) stay owned by the developer — the correct behaviour under rootless
// podman / Linux docker.
//
// SANDBOXER_CONTAINER_USER overrides it: this is the escape hatch for macOS,
// where the engine runs Linux containers inside a VM (Docker Desktop /
// podman-machine) and the host uid (e.g. 501) may not map cleanly through the
// VM's file-sharing layer. Set it to a "uid[:gid]" to pin a specific mapping, or
// to the empty string to omit --user entirely (let the container run as the
// engine's default). See docs/macos.md. The default is unchanged, so Linux argv
// and every existing session's ConfigHash are untouched.
func containerUserArgs() []string {
	if v, ok := os.LookupEnv("SANDBOXER_CONTAINER_USER"); ok {
		if v == "" {
			return nil
		}
		return []string{"--user", v}
	}
	return []string{"--user", strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())}
}

// Test seams: overridable so ensureImage can be unit-tested without a real
// engine or a multi-minute nix build.
var (
	imageExists = ImageExists
	buildImage  = toolbox.BuildImage
)

// ensureProxyImage guarantees the egress squid image is present before the
// sidecar starts. It is built locally beside the toolbox image (a fresh
// `sandboxer image build`, or the toolbox auto-build, produces both), so a
// missing one is a clear actionable error rather than a doomed engine pull.
func ensureProxyImage(o RunOpts) error {
	img := config.ProxyImage()
	if o.Engine == "" || imageExists(o.Engine, img) {
		return nil
	}
	return fmt.Errorf("egress proxy image %q not found — build it with:\n  sandboxer image build", img)
}

// ensureImage guarantees o.Image is present before the run. The bundled default
// image is never published: when it is missing we auto-build it (unless
// SANDBOXER_NO_AUTOBUILD is set, in which case we fail fast with a build hint).
// A custom SANDBOXER_IMAGE is assumed to be a real, pullable reference and is
// left to the engine to fetch as before.
func ensureImage(o RunOpts) error {
	if o.Engine == "" || o.Image == "" || imageExists(o.Engine, o.Image) {
		return nil
	}
	// The default image and content-addressed variants are both built locally
	// (never published); a truly custom SANDBOXER_IMAGE with an empty spec is
	// left for the engine to pull.
	if o.Image != config.DefaultImage && o.Spec.Empty() {
		return nil
	}
	// A bare `sandboxer image build` builds the STOCK image — a missing var-
	// variant needs its profile named, or the hint cannot fix the error.
	hint := "sandboxer image build"
	if !o.Spec.Empty() {
		hint = "sandboxer image build <profile> (this variant image needs its profile, by name or -f)"
	}
	if os.Getenv("SANDBOXER_NO_AUTOBUILD") != "" {
		return fmt.Errorf("toolbox image %q is not present and is built locally "+
			"(never published) — build it with:\n  %s", o.Image, hint)
	}
	if o.Stderr != nil {
		fmt.Fprintf(o.Stderr, "sandboxer: toolbox image %q not found — building it now "+
			"(one-time, several minutes; disable with SANDBOXER_NO_AUTOBUILD=1)…\n", o.Image)
	}
	if err := buildImage(toolbox.BuildOpts{
		Engine: o.Engine, Image: o.Image, Spec: o.Spec,
		Stdout: o.Stderr, Stderr: o.Stderr,
	}); err != nil {
		return fmt.Errorf("%w — build manually with: %s", err, hint)
	}
	if !imageExists(o.Engine, o.Image) {
		return fmt.Errorf("toolbox image %q still missing after build — try: %s", o.Image, hint)
	}
	return nil
}

// ImageExists reports whether the engine has the image locally. `image inspect`
// is supported by both docker and podman and exits non-zero when the image is
// absent.
func ImageExists(engine, image string) bool {
	return exec.Command(engine, "image", "inspect", image).Run() == nil
}

// ImageID returns the engine's local ID for image (the 64-hex sha256 digest,
// without docker's "sha256:" prefix — podman omits it), or "" on any failure.
// A locally absent image is "unknown", never an error: callers skip the
// image-freshness check on "" instead of failing before the image is built.
func ImageID(engine, image string) string {
	out, err := exec.Command(engine, "image", "inspect", "--format", "{{.Id}}", image).Output()
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSpace(string(out)), "sha256:")
}

// RemoveImage force-removes a local image by name/tag (force so a stopped
// container referencing it does not block removal). An already-absent image is
// success — `image rm` is idempotent — so only a real engine failure errors.
func RemoveImage(engine, image string) error {
	if !ImageExists(engine, image) {
		return nil
	}
	return execx.Run(engine, "image", "rm", "-f", image)
}

// exitCode maps a command error to a process exit code (0 success, the child's
// code for a non-zero exit, 1 for failure to start).
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}

// cpusFromQuota converts a CPU limit to the engine's --cpus value. It accepts a
// systemd-style quota ("100%", "150%") and converts it to a core count ("1",
// "1.5"); a plain value (already a core count like "1.5") is passed through. An
// empty or unparseable input yields "" (no limit).
func cpusFromQuota(s string) string {
	if s == "" {
		return ""
	}
	if pct, ok := strings.CutSuffix(s, "%"); ok {
		n, err := strconv.ParseFloat(pct, 64)
		if err != nil {
			return ""
		}
		return strconv.FormatFloat(n/100, 'f', -1, 64)
	}
	return s
}

// IsInteractiveTerminal reports whether v is a terminal a person could answer
// a prompt on — stricter than IsTerminal, which only asks for a character
// device (/dev/null is one). See isInteractiveTerminal.
func IsInteractiveTerminal(v any) bool {
	f, ok := v.(*os.File)
	return ok && isInteractiveTerminal(f)
}

func IsTerminal(v any) bool {
	f, ok := v.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
