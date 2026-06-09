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
	"github.com/irasikhin/sandboxer/internal/toolbox"
)

// RunOpts configures a single container invocation.
type RunOpts struct {
	Engine          string
	Image           string
	Dest            string // sandbox copy dir, mounted and used as workdir
	Slug            string
	HomeDir         string // sandbox-private agent home, mounted as $HOME (isolated per sandbox)
	RT              config.Runtime
	Profile         *config.Profile
	ProfileJSONPath string // mounted ro at /run/sandboxer/profile.json if present
	ManifestPath    string // mounted rw at /run/sandboxer/manifest.json if present
	Interactive     bool
	NoEgress        bool   // SANDBOXER_NO_EGRESS
	Mem             string // memory cap → --memory (e.g. 2G); empty = unlimited
	CPU             string // CPU cap → --cpus (accepts a float or systemd "100%")
	Wall            string // wall-clock timeout in seconds; empty = none
	Args            []string
	Stdin           io.Reader
	Stdout          io.Writer
	Stderr          io.Writer
}

// Run executes the container, wiring isolation, credentials, dependency mounts
// and (when applicable) the egress allowlist. It returns the container exit
// code.
func Run(o RunOpts) (int, error) {
	// Make sure the toolbox image is present before anything else (egress's
	// sidecar uses the same image). The bundled default is never published, so
	// a missing one is auto-built instead of letting the engine attempt a
	// doomed pull and drop the user back to the host shell.
	if err := ensureImage(o); err != nil {
		return 0, err
	}

	var eg *egress.Egress
	// Egress allowlist is required unless explicitly disabled (NoEgress /
	// egress: false) or an upstream proxy is holding the boundary. When it is
	// required we fail closed: we never silently fall back to an open bridge
	// network, because that would drop the isolation the caller asked for.
	egressRequired := !o.NoEgress && o.RT.Egress && o.RT.HTTPProxy == "" && o.RT.HTTPSProxy == ""
	if egressRequired {
		if len(o.RT.Domains) == 0 {
			return 0, fmt.Errorf("egress allowlist is enabled but no domains are allowed — " +
				"set --allow-domains / network.allowedDomains, or disable egress " +
				"(egress: false, or SANDBOXER_NO_EGRESS=1)")
		}
		e, err := egress.Up(o.Engine, o.Image, o.Slug, o.RT.Domains, o.Stderr)
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
	userns := strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())

	args := []string{"run", "--rm"}
	if o.Interactive {
		args = append(args, "-i")
		// -t only with a real TTY (docker errors "not a TTY" in pipes/CI).
		if isTerminal(o.Stdin) && isTerminal(o.Stdout) {
			args = append(args, "-t")
		}
	}
	args = append(args,
		"--user", userns, "--cap-drop=ALL", "--security-opt", "no-new-privileges",
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
	if o.Engine == "podman" {
		args = append(args, "--userns=keep-id")
	}
	// Resource limits (the banner advertises these on every backend).
	if o.Mem != "" {
		args = append(args, "--memory", o.Mem)
	}
	if cpus := cpusFromQuota(o.CPU); cpus != "" {
		args = append(args, "--cpus", cpus)
	}
	if egNet != "" {
		args = append(args, "--network", egNet,
			"--env", "HTTP_PROXY="+egProxyURL, "--env", "http_proxy="+egProxyURL,
			"--env", "HTTPS_PROXY="+egProxyURL, "--env", "https_proxy="+egProxyURL,
			"--env", "NO_PROXY=localhost,127.0.0.1", "--env", "no_proxy=localhost,127.0.0.1")
	}
	if o.ProfileJSONPath != "" && pathExists(o.ProfileJSONPath) {
		args = append(args, "--volume", o.ProfileJSONPath+":/run/sandboxer/profile.json:ro")
	}
	if o.ManifestPath != "" && pathExists(o.ManifestPath) {
		args = append(args, "--volume", o.ManifestPath+":/run/sandboxer/manifest.json:rw")
	}
	// Upstream proxy (corporate) takes precedence over the egress sidecar.
	if o.RT.HTTPProxy != "" {
		args = append(args, "--env", "HTTP_PROXY="+o.RT.HTTPProxy, "--env", "http_proxy="+o.RT.HTTPProxy)
	}
	if o.RT.HTTPSProxy != "" {
		args = append(args, "--env", "HTTPS_PROXY="+o.RT.HTTPSProxy, "--env", "https_proxy="+o.RT.HTTPSProxy)
	}
	if o.RT.NoProxy != "" {
		args = append(args, "--env", "NO_PROXY="+o.RT.NoProxy, "--env", "no_proxy="+o.RT.NoProxy)
	}
	if csv := o.RT.DomainsCSV(); csv != "" {
		args = append(args, "--env", "SANDBOXER_ALLOW_DOMAINS="+csv)
	}
	args = append(args, authEnvFlags(o.RT.AuthAgents)...)
	args = append(args, originMounts(o.ManifestPath)...)
	args = append(args, extraMountsAndEnv(o.Profile)...)
	args = append(args, o.Image)
	// Wall-clock timeout: wrap the in-container command with `timeout` (coreutils,
	// present in the toolbox image).
	if o.Wall != "" {
		args = append(args, "timeout", "--signal=TERM", o.Wall)
	}
	args = append(args, o.Args...)
	return args, nil
}

// Test seams: overridable so ensureImage can be unit-tested without a real
// engine or a multi-minute nix build.
var (
	imageExists = ImageExists
	buildImage  = toolbox.BuildImage
)

// ensureImage guarantees o.Image is present before the run. The bundled default
// image is never published: when it is missing we auto-build it (unless
// SANDBOXER_NO_AUTOBUILD is set, in which case we fail fast with a build hint).
// A custom SANDBOXER_IMAGE is assumed to be a real, pullable reference and is
// left to the engine to fetch as before.
func ensureImage(o RunOpts) error {
	if o.Engine == "" || o.Image == "" || imageExists(o.Engine, o.Image) {
		return nil
	}
	if o.Image != config.DefaultImage {
		return nil // custom image: let the engine pull it
	}
	if os.Getenv("SANDBOXER_NO_AUTOBUILD") != "" {
		return fmt.Errorf("toolbox image %q is not present and is built locally "+
			"(never published) — build it with:\n  sandboxer build-image", o.Image)
	}
	if o.Stderr != nil {
		fmt.Fprintf(o.Stderr, "sandboxer: toolbox image %q not found — building it now "+
			"(one-time, several minutes; disable with SANDBOXER_NO_AUTOBUILD=1)…\n", o.Image)
	}
	if err := buildImage(toolbox.BuildOpts{
		Engine: o.Engine, Image: o.Image,
		Stdout: o.Stderr, Stderr: o.Stderr,
	}); err != nil {
		return fmt.Errorf("%w — build manually with: sandboxer build-image", err)
	}
	if !imageExists(o.Engine, o.Image) {
		return fmt.Errorf("toolbox image %q still missing after build — try: sandboxer build-image", o.Image)
	}
	return nil
}

// ImageExists reports whether the engine has the image locally. `image inspect`
// is supported by both docker and podman and exits non-zero when the image is
// absent.
func ImageExists(engine, image string) bool {
	return exec.Command(engine, "image", "inspect", image).Run() == nil
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
