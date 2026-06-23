package config

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/irasikhin/sandboxer/internal/registry"
)

// Runtime is the fully-resolved set of effective settings for one sandbox
// invocation (the bash load_runtime result). It carries no filesystem state.
type Runtime struct {
	// Proxy is the resolved proxy URL (empty = none). With Egress on it is the
	// parent the allowlist sidecar chains through; with Egress off the agent
	// talks to it directly. The backend rewrites a localhost host to the host
	// gateway before use (see backend.ContainerProxyURL).
	Proxy      string
	NoProxy    string   // NO_PROXY, applied only in direct mode (Egress off)
	Domains    []string // resolved egress allowlist
	Model      string
	Agent      string
	Backend    string
	Session    string   // SessionPersistent or SessionEphemeral (resolved; never empty)
	AuthAgents []string // whose creds to bind in the container
	Egress     bool
}

// Session modes for Runtime.Session: a persistent detached container reused
// across enter/exec invocations, or a fresh one-shot container per command.
const (
	SessionPersistent = "persistent"
	SessionEphemeral  = "ephemeral"
)

// Overrides are command-line flag values; empty means "not set".
type Overrides struct {
	Model   string
	Agent   string
	Backend string
	Session string // SessionEphemeral when --ephemeral is given
	Domains string // csv
}

// ResolveRuntime applies the precedence flags > profile > base(run.env)/defaults.
// baseDomains and baseModel come from run.env (themselves seeded from defaults).
// Returns an error when a configured domain fails validation (e.g. a typo that
// would silently deny the agent's traffic).
func ResolveRuntime(p *Profile, d Defaults, baseDomains, baseModel string, f Overrides) (Runtime, error) {
	if p == nil {
		p = &Profile{}
	}
	rt := Runtime{
		Proxy:   firstNonEmpty(p.Proxy, d.Proxy),
		NoProxy: firstNonEmpty(p.NoProxy, d.NoProxy),
		Egress:  p.EgressEnabled(),
	}
	if err := ValidateProxy(rt.Proxy, rt.Egress); err != nil {
		return Runtime{}, err
	}
	if err := ValidateImageSpec(p.Image); err != nil {
		return Runtime{}, err
	}

	domains := f.Domains
	if domains == "" {
		domains = strings.Join(p.Network.AllowedDomains, ",")
	}
	if domains == "" {
		domains = baseDomains
	}
	rt.Domains = splitCSV(domains)
	if err := ValidateDomains(rt.Domains); err != nil {
		return Runtime{}, err
	}

	rt.Model = firstNonEmpty(f.Model, p.Model, baseModel)
	rt.Agent = firstNonEmpty(f.Agent, p.Agent, d.Agent)
	rt.Backend = firstNonEmpty(f.Backend, p.Backend, d.Backend)
	// Session deviates from the others: the env (d.Session) sits ABOVE the
	// profile, because SANDBOXER_SESSION=ephemeral is an operator kill-switch
	// that must win over a profile's `session:` choice.
	rt.Session = firstNonEmpty(f.Session, d.Session, p.Session, SessionPersistent)

	if len(p.Agents) > 0 {
		rt.AuthAgents = append([]string{}, p.Agents...)
	} else {
		rt.AuthAgents = registry.Names()
	}
	return rt, nil
}

// DomainsCSV joins the resolved allowlist back to a comma-separated string.
func (r Runtime) DomainsCSV() string { return strings.Join(r.Domains, ",") }

// ValidateDomains checks each domain for common typos (missing dot, spaces,
// protocol prefix, path components). It returns nil for an empty list and the
// first invalid domain found together with a hint.
func ValidateDomains(domains []string) error {
	for _, d := range domains {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if strings.Contains(d, " ") {
			return fmt.Errorf("invalid domain %q — contains whitespace", d)
		}
		if strings.HasPrefix(d, "http://") || strings.HasPrefix(d, "https://") {
			return fmt.Errorf("invalid domain %q — give a hostname, not a URL", d)
		}
		if strings.Contains(d, "/") {
			return fmt.Errorf("invalid domain %q — give a hostname, not a path", d)
		}
		if !strings.Contains(d, ".") {
			return fmt.Errorf("invalid domain %q — missing dot (did you mean something like %s.com?)", d, d)
		}
	}
	return nil
}

// ValidateBackend rejects an unsupported isolation backend. sandboxer runs every
// agent inside a podman/docker container; an empty value means auto-detect the
// engine. The native (host /sandbox) backend was removed — a stale
// "backend: native" gets a clear migration error instead of silently running a
// container.
func ValidateBackend(rt Runtime) error {
	switch rt.Backend {
	case "", "auto", "podman", "docker":
		return nil
	case "native":
		return fmt.Errorf("the native backend was removed — sandboxer is container-only now; use backend: docker or podman")
	default:
		return fmt.Errorf("unknown backend %q — use docker or podman", rt.Backend)
	}
}

// ValidateSession rejects an unknown session mode. The resolved value always
// carries a mode (the default is persistent), so anything else is a typo in
// the flag, SANDBOXER_SESSION or the profile's `session:` field.
func ValidateSession(rt Runtime) error {
	switch rt.Session {
	case SessionPersistent, SessionEphemeral:
		return nil
	default:
		return fmt.Errorf("unknown session mode %q — use persistent or ephemeral", rt.Session)
	}
}

// ValidateProxy rejects a malformed proxy URL. The proxy must parse to an
// http:// or https:// URL with a host. When the egress allowlist is on the proxy
// is chained through the sidecar (squid cache_peer), which cannot speak TLS to a
// parent yet — so an https:// proxy is only accepted with egress off (direct
// mode). An empty proxy is valid (no proxy).
func ValidateProxy(proxyURL string, egress bool) error {
	if proxyURL == "" {
		return nil
	}
	u, err := url.Parse(proxyURL)
	if err != nil {
		return fmt.Errorf("invalid proxy %q: %w", proxyURL, err)
	}
	if u.Host == "" {
		return fmt.Errorf("invalid proxy %q — expected an http://host:port URL", proxyURL)
	}
	switch u.Scheme {
	case "http":
		return nil
	case "https":
		if egress {
			return fmt.Errorf("proxy %q uses https — with the egress allowlist on only an http:// proxy "+
				"works (the sidecar chains over http); set egress: false to use it as a direct https proxy", proxyURL)
		}
		return nil
	default:
		return fmt.Errorf("invalid proxy %q — expected an http://host:port URL", proxyURL)
	}
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
