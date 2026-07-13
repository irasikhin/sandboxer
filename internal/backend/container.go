package backend

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/egress"
	"github.com/irasikhin/sandboxer/internal/toolbox"
)

// hostGatewayAlias is the hostname that resolves to the host from inside a
// container on either engine (commonArgs maps it via --add-host=host-gateway).
// A proxy a user runs "on localhost" is really on the host, which is NOT the
// container's own loopback — so ContainerProxyURL rewrites a localhost proxy to
// this name and the egress sidecar gets the same --add-host (see egress.Up).
const hostGatewayAlias = "host.docker.internal"

// ContainerProxyURL adapts a configured proxy URL for use from inside a
// container: a host of localhost / 127.0.0.1 / ::1 is rewritten to the host
// gateway (hostGatewayAlias), since the user means "a proxy on my host", not the
// container's own loopback. Any other host (a real hostname or LAN IP) is left
// untouched. An empty or unparseable URL is returned unchanged.
func ContainerProxyURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	host := u.Hostname()
	switch host {
	case "localhost", "127.0.0.1", "::1":
		port := u.Port()
		newHost := hostGatewayAlias
		if port != "" {
			newHost += ":" + port
		}
		u.Host = newHost
		return u.String()
	default:
		return raw
	}
}

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
	Engine          string
	Image           string
	Spec            toolbox.Spec // image variant customization; drives the auto-build of a missing variant
	Dest            string       // sandbox working tree (git worktree or copy dir), mounted and used as workdir
	GitCommonDir    string       // shared git dir bind-mounted so git works in-container; "" when unset
	GitUserName     string       // host-resolved git identity, injected so the agent can commit
	GitUserEmail    string       // without writing to (now read-only) repo config
	Slug            string
	BaseDir         string // host .sandboxer dir; names/labels the persistent session (zero value fine for one-shot runs)
	HomeDir         string // sandbox-private agent home, mounted as $HOME (isolated per sandbox)
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
	// egress: false) or an upstream proxy is holding the boundary. When it is
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
				"refusing to run on an open network (disable with egress: false or SANDBOXER_NO_EGRESS=1)", err)
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
		if isTerminal(o.Stdin) && isTerminal(o.Stdout) {
			args = append(args, "-t")
		}
	}
	args = append(args, commonArgs(o, egNet, egProxyURL)...)
	args = append(args, o.Image)
	args = append(args, o.Args...)
	return args, nil
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
		"--workdir", o.Dest, "--volume", o.Dest+":"+o.Dest+":rw",
		"--env", "SANDBOXER_IN_CONTAINER=1",
		"--env", "SANDBOXER_SLUG="+o.Slug, "--env", "SANDBOXER_SANDBOX_DIR="+o.Dest,
	)
	// $HOME is the sandbox-private agent home, bound at its own host path. It is
	// isolated per sandbox (see sandbox.Base.HomeDir): the host's real home is
	// never mounted, so no host config leaks in and the agent's atomic config
	// rewrites (e.g. ~/.claude.json) land on a real directory rather than a
	// bind-mounted file. The image has no /home, so there is nothing to shadow.
	if o.HomeDir != "" {
		args = append(args, "--env", "HOME="+o.HomeDir,
			"--volume", o.HomeDir+":"+o.HomeDir+":rw")
	}
	// In git-worktree mode the shared (common) git dir is bind-mounted at its own
	// host path so the worktree's gitdir pointer and object store resolve inside
	// the container — the agent gets a real git (branch, commit, diff). The mount
	// is writable (git must update objects/refs/logs/worktrees to commit), but the
	// two paths git executes as commands on the HOST — `config` (core.hooksPath,
	// core.fsmonitor, filter.*.clean/smudge, core.pager, gpg.program, …) and
	// `hooks/` — are re-mounted read-only, so a compromised agent cannot plant
	// host code that runs on the developer's next git command. safe.directory=*
	// clears git's dubious-ownership guard (the repo is host-owned, the container
	// is a mapped user); hooks are additionally disabled in-container as
	// defense-in-depth; and the host's git identity is injected so the agent can
	// commit without writing to the now-read-only config. See SECURITY.md.
	// An empty GitCommonDir leaves the argv (and ConfigHash) untouched.
	if o.GitCommonDir != "" {
		cd := o.GitCommonDir
		args = append(args, "--volume", cd+":"+cd+":rw",
			"--volume", roMount(cd, "config"),
			"--volume", roMount(cd, "hooks"))
		gc := []string{
			"safe.directory", "*",
			"core.hooksPath", "/dev/null",
			"core.fsmonitor", "false",
		}
		if o.GitUserName != "" {
			gc = append(gc, "user.name", o.GitUserName)
		}
		if o.GitUserEmail != "" {
			gc = append(gc, "user.email", o.GitUserEmail)
		}
		args = append(args, gitConfigEnv(gc)...)
	}
	if o.Engine == "podman" {
		args = append(args, "--userns=keep-id")
	}
	// Map both engines' host-gateway alias so a single hostname reaches a
	// host-running service (e.g. a user's own proxy) from inside the container,
	// regardless of engine: podman provides host.containers.internal and Docker
	// Desktop provides host.docker.internal, but Linux Docker resolves neither
	// without this. Harmless on the --internal egress network (the name just
	// resolves to an unreachable gateway). Requires podman >= 4 / docker >= 20.10.
	args = append(args,
		"--add-host=host.docker.internal:host-gateway",
		"--add-host=host.containers.internal:host-gateway")
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
	args = append(args, authEnvFlags(o.RT.AuthAgents)...)
	args = append(args, extraMountsAndEnv(o.Profile)...)
	return args
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

// roMount builds the `--volume` value that re-mounts sub (a child of the shared
// git dir dir) read-only on top of the writable git-dir mount, so the agent
// cannot write it. sub always exists in a git dir (config, hooks/), so the
// engine binds the real path rather than auto-creating a root-owned placeholder.
func roMount(dir, sub string) string {
	p := filepath.Join(dir, sub)
	return p + ":" + p + ":ro"
}

// gitConfigEnv renders key/value pairs as the GIT_CONFIG_COUNT / GIT_CONFIG_KEY_i
// / GIT_CONFIG_VALUE_i --env flags git reads as ephemeral config (highest
// precedence below `git -c`), so values are injected without touching any config
// file. pairs must have even length (k0, v0, k1, v1, …).
func gitConfigEnv(pairs []string) []string {
	n := len(pairs) / 2
	out := []string{"--env", "GIT_CONFIG_COUNT=" + strconv.Itoa(n)}
	for i := 0; i < n; i++ {
		idx := strconv.Itoa(i)
		out = append(out,
			"--env", "GIT_CONFIG_KEY_"+idx+"="+pairs[2*i],
			"--env", "GIT_CONFIG_VALUE_"+idx+"="+pairs[2*i+1])
	}
	return out
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
	return exec.Command(engine, "image", "rm", "-f", image).Run()
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

func isTerminal(v any) bool {
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
