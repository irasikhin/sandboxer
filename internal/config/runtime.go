package config

import (
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
func ResolveRuntime(p *Profile, d Defaults, baseDomains, baseModel string, f Overrides) Runtime {
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

	rt.Model = firstNonEmpty(f.Model, p.Model, baseModel)
	rt.Agent = firstNonEmpty(f.Agent, p.Agent, d.Agent)
	rt.Backend = firstNonEmpty(f.Backend, p.Backend, d.Backend)

	if len(p.Agents) > 0 {
		rt.AuthAgents = append([]string{}, p.Agents...)
	} else {
		rt.AuthAgents = registry.Names()
	}
	return rt
}

// DomainsCSV joins the resolved allowlist back to a comma-separated string.
func (r Runtime) DomainsCSV() string { return strings.Join(r.Domains, ",") }

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
