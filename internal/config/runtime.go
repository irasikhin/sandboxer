package config

import (
	"fmt"
	"strings"

	"github.com/irasikhin/sandboxer/internal/registry"
)

// Runtime is the fully-resolved set of effective settings for one sandbox
// invocation (the bash load_runtime result). It carries no filesystem state.
type Runtime struct {
	HTTPProxy  string
	HTTPSProxy string
	NoProxy    string
	Domains    []string // resolved egress allowlist
	Model      string
	Agent      string
	Backend    string
	AuthAgents []string // whose creds to bind in the container
	Egress     bool
}

// Overrides are command-line flag values; empty means "not set".
type Overrides struct {
	Model   string
	Agent   string
	Backend string
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
		HTTPProxy:  p.Proxy.HTTP,
		HTTPSProxy: p.Proxy.HTTPS,
		NoProxy:    p.Proxy.No,
		Egress:     p.EgressEnabled(),
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
		return fmt.Errorf("the native backend was removed — sandboxer is container-only now; use backend: podman or docker")
	default:
		return fmt.Errorf("unknown backend %q — use podman or docker", rt.Backend)
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
