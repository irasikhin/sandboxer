// Package egress brings up the container egress allowlist: the agent runs on an
// --internal network with no direct outbound, and its sole exit is a forward
// proxy that only permits the configured domains. The proxy is the sandboxer
// binary itself (baked into the toolbox image) running in `_proxy` mode, so
// there is no external proxy dependency.
package egress

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"

	"github.com/irasikhin/sandboxer/internal/config"
)

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

// Up creates the networks and starts the allowlist proxy sidecar. On any
// failure it tears down whatever it created and returns an error.
func Up(engine, image, slug string, domains []string, w io.Writer) (*Egress, error) {
	if len(domains) == 0 {
		return nil, fmt.Errorf("egress: no allowlist domains")
	}
	id := fmt.Sprintf("sbx-%s-%d", config.Sanitize(slug), os.Getpid())
	e := &Egress{
		engine: engine,
		net:    id + "-int",
		extNet: id + "-ext",
		proxy:  id + "-proxy",
	}
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

func (e *Egress) run(args ...string) error {
	cmd := exec.Command(e.engine, args...)
	return cmd.Run()
}
