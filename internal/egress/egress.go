// Package egress brings up the container egress allowlist: the agent runs on an
// --internal network with no direct outbound, and its sole exit is a forward
// proxy that only permits the configured domains. The proxy is a minimal squid
// (config.ProxyImage) running a generated squid.conf — a stock proxy, NOT the
// sandboxer binary, so nothing reaches the host through sandboxer and the
// toolbox image needs no binary baked in.
package egress

import (
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/irasikhin/sandboxer/internal/execx"
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
	conf   string // host path of the generated squid.conf (removed on Down)
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
// traffic through that parent proxy (the allowlist is still enforced). confDir
// is where the generated squid.conf is written (the session's state dir for a
// persistent session; "" falls back to a temp dir). routes send specific
// domains through their own upstream (see squidConf). On any failure it tears
// down whatever it created and returns an error.
func Up(engine, slug string, domains []string, upstream string, routes []config.Route, confDir string, w io.Writer) (*Egress, error) {
	id := fmt.Sprintf("sbx-%s-%d", config.Sanitize(slug), os.Getpid())
	return upWithID(engine, id, domains, upstream, routes, confDir, w)
}

// UpNamed is Up with caller-chosen stable resource names (<id>-int, <id>-ext,
// <id>-proxy), for persistent sessions that must rediscover their egress from
// a later CLI invocation. It is only called when (re)creating the session
// container, so any same-named resources are leftovers from a previous life —
// they are removed first (Down semantics) so the creates cannot collide.
func UpNamed(engine, id string, domains []string, upstream string, routes []config.Route, confDir string, w io.Writer) (*Egress, error) {
	if len(domains) == 0 {
		return nil, errNoDomains
	}
	Lookup(engine, id).Down()
	return upWithID(engine, id, domains, upstream, routes, confDir, w)
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
// (ephemeral, PID-suffixed id) and UpNamed (stable id) funnel through it. It
// writes a generated squid.conf (the domain allowlist) and bind-mounts it into
// the squid sidecar. The writer is part of the seam for progress output but
// currently unused.
func upWithID(engine, id string, domains []string, upstream string, routes []config.Route, confDir string, _ io.Writer) (*Egress, error) {
	if len(domains) == 0 {
		return nil, errNoDomains
	}
	e := Lookup(engine, id)
	if confDir == "" {
		confDir = os.TempDir()
	}
	e.conf = filepath.Join(confDir, id+"-squid.conf")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		return nil, fmt.Errorf("egress: %w", err)
	}
	if err := os.WriteFile(e.conf, []byte(squidConf(domains, upstream, routes)), 0o644); err != nil {
		return nil, fmt.Errorf("egress: write squid.conf: %w", err)
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
	// The proxy image's entrypoint runs `squid -N -f /etc/sandboxer/squid.conf`;
	// we only bind-mount the generated config. cap-drop=ALL + the unprivileged
	// run user are fine because the config keeps no on-disk cache and logs to
	// std streams (no writable image dirs needed).
	runArgs := []string{
		"run", "-d", "--name", e.proxy, "--network", e.net,
		"--user", userns, "--cap-drop=ALL", "--security-opt", "no-new-privileges",
		"--volume", e.conf + ":/etc/sandboxer/squid.conf:ro",
		// Map the host gateway INSIDE the sidecar too. When a proxy chains through
		// squid (cache_peer below), it is the sidecar — not the agent container —
		// that dials the parent. A user's proxy on the host is addressed as
		// host.docker.internal / host.containers.internal, which Linux Docker only
		// resolves with an explicit --add-host (Docker Desktop / podman provide one
		// of them, but not both, so map both). Without this a host proxy is
		// unreachable from the sidecar and all chained egress dies.
		"--add-host=host.docker.internal:host-gateway",
		"--add-host=host.containers.internal:host-gateway",
		config.ProxyImage(),
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

// squidConf renders the squid configuration enforcing the domain allowlist:
// only the listed domains (and their subdomains) are reachable over HTTP and
// HTTPS (CONNECT to 443); everything else is denied. It keeps no on-disk cache
// and logs to std streams so squid runs as an unprivileged, capability-dropped
// sidecar with no writable image dirs.
//
// Upstream chaining, in precedence order:
//   - each route's domains go through that route's own parent (cache_peer
//     sbxpeerN) and never direct — a routed peer being down fails closed (503),
//     never leaks;
//   - a non-empty default upstream chains everything else through that parent
//     (denied for every routed acl so routed domains keep their own peer);
//   - with routes but no default upstream, unrouted allowed traffic goes DIRECT.
//
// Route order is deterministic (as listed) so the config — and its fingerprint —
// is stable.
func squidConf(domains []string, upstream string, routes []config.Route) string {
	var b strings.Builder
	b.WriteString("http_port 8888\n")
	b.WriteString("acl allowed dstdomain")
	writeDomains(&b, domains)
	b.WriteString("\n")
	for i, r := range routes {
		fmt.Fprintf(&b, "acl sbxroute%d dstdomain", i)
		writeDomains(&b, r.Domains)
		b.WriteString("\n")
	}
	b.WriteString("acl SSL_ports port 443\n")
	b.WriteString("acl CONNECT method CONNECT\n")
	b.WriteString("http_access deny CONNECT !SSL_ports\n")
	b.WriteString("http_access allow allowed\n")
	b.WriteString("http_access deny all\n")
	b.WriteString("cache deny all\n")
	b.WriteString("access_log stdio:/dev/stdout\n")
	b.WriteString("cache_log stdio:/dev/stderr\n")
	b.WriteString("pid_filename none\n")
	b.WriteString("coredump_dir /tmp\n")
	b.WriteString("logfile_rotate 0\n")
	// Route peers: each routed domain set gets its own parent and never goes
	// direct (fail-closed if that parent is down).
	for i, r := range routes {
		host, port, ok := parseUpstream(r.Proxy)
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "cache_peer %s parent %s 0 no-query name=sbxpeer%d\n", host, port, i)
		fmt.Fprintf(&b, "cache_peer_access sbxpeer%d allow sbxroute%d\n", i, i)
		fmt.Fprintf(&b, "cache_peer_access sbxpeer%d deny all\n", i)
		fmt.Fprintf(&b, "never_direct allow sbxroute%d\n", i)
	}
	if host, port, ok := parseUpstream(upstream); ok {
		if len(routes) == 0 {
			// Unchanged legacy output: one default peer, all traffic chained.
			fmt.Fprintf(&b, "cache_peer %s parent %s 0 no-query default\n", host, port)
			b.WriteString("never_direct allow all\n")
		} else {
			// The default peer serves everything the routes don't claim.
			fmt.Fprintf(&b, "cache_peer %s parent %s 0 no-query default name=sbxdefault\n", host, port)
			for i := range routes {
				fmt.Fprintf(&b, "cache_peer_access sbxdefault deny sbxroute%d\n", i)
			}
			b.WriteString("cache_peer_access sbxdefault allow all\n")
			b.WriteString("never_direct allow all\n")
		}
	}
	return b.String()
}

// writeDomains appends each non-empty domain to b as a space-prefixed
// leading-dot dstdomain token (a leading dot matches the domain AND subdomains).
//
// Defence-in-depth: only a clean hostname token is ever emitted. The config
// layer already rejects anything else with an error (config.ValidateDomains),
// but skipping a non-hostname value HERE too structurally guarantees a domain
// can never carry a newline or tab that would inject a squid.conf directive
// (e.g. an `http_access allow all` above the deny), regardless of how the value
// reached this function.
func writeDomains(b *strings.Builder, domains []string) {
	for _, d := range domains {
		d = strings.TrimSpace(d)
		if d == "" || !isCleanDomainToken(d) {
			continue
		}
		if !strings.HasPrefix(d, ".") {
			d = "." + d
		}
		b.WriteString(" ")
		b.WriteString(d)
	}
}

// isCleanDomainToken reports whether d consists solely of the characters a DNS
// hostname (or its leading-dot subdomain form) may contain, so it can be written
// into squid.conf without introducing a new token or directive.
func isCleanDomainToken(d string) bool {
	for _, r := range d {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-':
		default:
			return false
		}
	}
	return true
}

// ConfFingerprint is the sha256 of the squid.conf Up would generate for the
// given allowlist, default upstream and routes. Folding it into a session's
// ConfigHash makes editing the domains/proxy/routes reconfigure the session
// (recreate) instead of silently taking effect only after a manual recreate.
func ConfFingerprint(domains []string, upstream string, routes []config.Route) string {
	sum := sha256.Sum256([]byte(squidConf(domains, upstream, routes)))
	return hex.EncodeToString(sum[:])
}

// parseUpstream splits an upstream proxy URL (http://host:port) into host and
// port for a squid cache_peer line. Returns ok=false for an empty or
// unparseable value (no upstream chaining then).
func parseUpstream(upstream string) (host, port string, ok bool) {
	if strings.TrimSpace(upstream) == "" {
		return "", "", false
	}
	u, err := url.Parse(upstream)
	if err != nil || u.Hostname() == "" {
		return "", "", false
	}
	port = u.Port()
	if port == "" {
		port = "3128"
	}
	return u.Hostname(), port, true
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
	if e.conf != "" {
		_ = os.Remove(e.conf)
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

// run executes one engine command; on failure the error carries the engine's
// own stderr, so a caller's wrapping context ("proxy sidecar failed to start")
// is followed by the reason rather than a bare "exit status 125".
func (e *Egress) run(args ...string) error {
	return execx.Run(e.engine, args...)
}
