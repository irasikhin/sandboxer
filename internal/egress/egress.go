// Package egress brings up the container egress allowlist: the agent runs on an
// --internal network with no direct outbound, and its sole exit is a forward
// proxy that only permits the configured domains. The proxy is the sandboxer
// binary itself (baked into the toolbox image) running in `_proxy` mode, so
// there is no external proxy dependency.
package egress

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/irasikhin/sandboxer/internal/config"
)

// errNoDomains rejects bringing up an allowlist with nothing on it: that is
// always a caller misconfiguration, not a useful "block everything" mode.
var errNoDomains = errors.New("egress: no allowlist domains")

// Egress holds the networks and proxy container backing one sandbox run.
type Egress struct {
	engine string
	net    string // --internal network the agent joins
	extNet string // external network giving the proxy outbound
	proxy  string // proxy container name
	active bool
}

// Net returns the internal network name the agent container should join.
func (e *Egress) Net() string { return e.net }

// ProxyURL returns the HTTP(S)_PROXY URL the agent should use.
func (e *Egress) ProxyURL() string { return "http://" + e.proxy + ":8888" }

// Active reports whether the allowlist was successfully brought up.
func (e *Egress) Active() bool { return e != nil && e.active }

// Up creates the networks and starts the allowlist proxy sidecar. The
// resources get per-invocation names (the PID keeps concurrent runs of the
// same sandbox apart). When upstream is non-empty the sidecar chains allowed
// traffic through that parent proxy (the allowlist is still enforced). On any
// failure it tears down whatever it created and returns an error.
func Up(engine, image, slug string, domains []string, upstream string, w io.Writer) (*Egress, error) {
	id := fmt.Sprintf("sbx-%s-%d", config.Sanitize(slug), os.Getpid())
	return upWithID(engine, image, id, domains, upstream, w)
}

// UpNamed is Up with caller-chosen stable resource names (<id>-int, <id>-ext,
// <id>-proxy), for persistent sessions that must rediscover their egress from
// a later CLI invocation. It is only called when (re)creating the session
// container, so any same-named resources are leftovers from a previous life —
// they are removed first (Down semantics) so the creates cannot collide.
func UpNamed(engine, image, id string, domains []string, upstream string, w io.Writer) (*Egress, error) {
	if len(domains) == 0 {
		return nil, errNoDomains
	}
	Lookup(engine, id).Down()
	return upWithID(engine, image, id, domains, upstream, w)
}

// Lookup constructs a handle onto the resources UpNamed(id) names — the proxy
// container and both networks — without touching the engine, so Down, Stop,
// Start, and ProxyRunning work without a prior Up. The handle never reports
// Active.
func Lookup(engine, id string) *Egress {
	return &Egress{
		engine: engine,
		net:    id + "-int",
		extNet: id + "-ext",
		proxy:  id + "-proxy",
	}
}

// upWithID brings up the allowlist under id-derived resource names; both Up
// (ephemeral, PID-suffixed id) and UpNamed (stable id) funnel through it. The
// writer is part of the seam for progress output but currently unused.
func upWithID(engine, image, id string, domains []string, upstream string, _ io.Writer) (*Egress, error) {
	if len(domains) == 0 {
		return nil, errNoDomains
	}
	e := Lookup(engine, id)
	steps := [][]string{
		{"network", "create", "--internal", e.net},
		{"network", "create", e.extNet},
	}
	for _, s := range steps {
		if err := e.run(s...); err != nil {
			e.Down()
			return nil, fmt.Errorf("egress: %w", err)
		}
	}
	userns := strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
	runArgs := []string{
		"run", "-d", "--name", e.proxy, "--network", e.net,
		"--user", userns, "--cap-drop=ALL", "--security-opt", "no-new-privileges",
		image, "sandboxer", "_proxy", "--listen", ":8888",
	}
	for _, d := range domains {
		runArgs = append(runArgs, "--allow", d)
	}
	if upstream != "" {
		runArgs = append(runArgs, "--upstream", upstream)
	}
	if err := e.run(runArgs...); err != nil {
		e.Down()
		return nil, fmt.Errorf("egress: proxy sidecar failed to start: %w", err)
	}
	if err := e.run("network", "connect", e.extNet, e.proxy); err != nil {
		e.Down()
		return nil, fmt.Errorf("egress: %w", err)
	}
	e.active = true
	return e, nil
}

// Down removes the proxy container and networks. Idempotent and best-effort.
func (e *Egress) Down() {
	if e == nil {
		return
	}
	if e.proxy != "" {
		_ = exec.Command(e.engine, "rm", "-f", e.proxy).Run()
	}
	if e.net != "" {
		_ = exec.Command(e.engine, "network", "rm", e.net).Run()
	}
	if e.extNet != "" {
		_ = exec.Command(e.engine, "network", "rm", e.extNet).Run()
	}
	e.active = false
}

// ProxyRunning reports whether the proxy container currently exists and is
// running (after a host reboot it persists but is stopped). Nil-safe.
func (e *Egress) ProxyRunning() bool {
	if e == nil {
		return false
	}
	out, err := exec.Command(e.engine, "container", "inspect", "--format", "{{.State.Running}}", e.proxy).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

// Stop stops the proxy container only; it and the networks stay in place so
// Start can resume the same egress later. Nil-safe: nothing to stop.
func (e *Egress) Stop() error {
	if e == nil {
		return nil
	}
	if err := e.run("stop", e.proxy); err != nil {
		return fmt.Errorf("egress: stop proxy %s: %w", e.proxy, err)
	}
	return nil
}

// Start starts a previously stopped proxy container; the networks it joined
// persist across stops, so no re-wiring is needed. Nil-safe: nothing to start.
func (e *Egress) Start() error {
	if e == nil {
		return nil
	}
	if err := e.run("start", e.proxy); err != nil {
		return fmt.Errorf("egress: start proxy %s: %w", e.proxy, err)
	}
	return nil
}

func (e *Egress) run(args ...string) error {
	cmd := exec.Command(e.engine, args...)
	return cmd.Run()
}
