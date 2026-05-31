package backend

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/egress"
)

// RunOpts configures a single container invocation.
type RunOpts struct {
	Engine          string
	Image           string
	Dest            string // sandbox copy dir, mounted and used as workdir
	Slug            string
	RT              config.Runtime
	Profile         *config.Profile
	ProfileJSONPath string // mounted ro at /run/sandboxer/profile.json if present
	ManifestPath    string // mounted rw at /run/sandboxer/manifest.json if present
	Interactive     bool
	Ephemeral       bool   // copy creds into EphDir instead of binding host dirs
	EphDir          string // temp dir for ephemeral creds (required if Ephemeral)
	NoEgress        bool   // SANDBOXER_NO_EGRESS
	Args            []string
	Stdin           io.Reader
	Stdout          io.Writer
	Stderr          io.Writer
}

// Run executes the container, wiring isolation, credentials, dependency mounts
// and (when applicable) the egress allowlist. It returns the container exit
// code.
func Run(o RunOpts) (int, error) {
	var eg *egress.Egress
	// Egress allowlist: only when not disabled, the profile allows it, there is
	// no upstream proxy holding the boundary, and we have domains to allow.
	if !o.NoEgress && o.RT.Egress && o.RT.HTTPProxy == "" && o.RT.HTTPSProxy == "" && len(o.RT.Domains) > 0 {
		e, err := egress.Up(o.Engine, o.Image, o.Slug, o.RT.Domains, o.Stderr)
		if err != nil {
			fmt.Fprintln(o.Stderr, "sandboxer: egress: allowlist proxy failed to start — container WITHOUT network isolation (bridge)")
		} else {
			eg = e
		}
	}
	defer eg.Down()

	home, _ := os.UserHomeDir()
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
		"--env", "HOME="+home, "--env", "SANDBOXER_IN_CONTAINER=1",
		"--env", "SANDBOXER_SLUG="+o.Slug, "--env", "SANDBOXER_SANDBOX_DIR="+o.Dest,
	)
	if o.Engine == "podman" {
		args = append(args, "--userns=keep-id")
	}
	if eg.Active() {
		url := eg.ProxyURL()
		args = append(args, "--network", eg.Net(),
			"--env", "HTTP_PROXY="+url, "--env", "http_proxy="+url,
			"--env", "HTTPS_PROXY="+url, "--env", "https_proxy="+url,
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
	args = append(args, authFlags(o.RT.AuthAgents, o.Ephemeral, o.EphDir)...)
	args = append(args, originMounts(o.ManifestPath)...)
	args = append(args, extraMountsAndEnv(o.Profile)...)
	args = append(args, o.Image)
	args = append(args, o.Args...)

	cmd := exec.Command(o.Engine, args...)
	cmd.Stdin = o.Stdin
	cmd.Stdout = o.Stdout
	cmd.Stderr = o.Stderr
	err := cmd.Run()
	return exitCode(err), nil
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
