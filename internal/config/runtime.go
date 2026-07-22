package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Runtime is the fully-resolved set of effective settings for one sandbox
// invocation (the bash load_runtime result). It carries no filesystem state.
type Runtime struct {
	// Proxy is the resolved proxy URL (empty = none). With Egress on it is the
	// parent the allowlist sidecar chains through; with Egress off the agent
	// talks to it directly. The backend rewrites a localhost host to the host
	// gateway before use (see backend.ContainerProxyURL).
	Proxy   string
	NoProxy string   // NO_PROXY, applied only in direct mode (Egress off)
	Domains []string // resolved egress allowlist
	Backend string
	Session string // SessionPersistent or SessionEphemeral (resolved; never empty)
	Egress  bool
	// Routes send specific domains through a dedicated upstream proxy (see
	// Egress.Routes). Applied only with Egress on; ignored in direct mode.
	Routes []Route
	// Resource caps threaded into the container run (--memory/--cpus/--pids-limit).
	// Mem/CPU resolve profile-over-env (limits: over SANDBOXER_MEM/SANDBOXER_CPU);
	// Pids has no env default. Empty/zero means uncapped.
	Mem  string
	CPU  string
	Pids int
	// AutoResume relaunches recorded agents when a saved session layout is
	// restored (profile autoResume, killed by SANDBOXER_NO_RESUME=1). Not part
	// of the create argv, so it never affects the session ConfigHash.
	AutoResume bool
}

// Session modes for Runtime.Session: a persistent detached container reused
// across enter/exec invocations, or a fresh one-shot container per command.
const (
	SessionPersistent = "persistent"
	SessionEphemeral  = "ephemeral"
)

// Overrides are command-line flag values; empty means "not set".
type Overrides struct {
	Backend string
	Session string // SessionEphemeral when --ephemeral is given
	Domains string // csv
}

// ResolveRuntime applies the precedence flags > profile > base(run.env)/defaults.
// baseDomains comes from run.env (itself seeded from defaults). Returns an error
// when a configured domain fails validation (e.g. a typo that would silently
// deny the agent's traffic).
func ResolveRuntime(p *Profile, d Defaults, baseDomains string, f Overrides) (Runtime, error) {
	if p == nil {
		p = &Profile{}
	}
	rt := Runtime{
		Proxy:   firstNonEmpty(p.Egress.Proxy, d.Proxy),
		NoProxy: firstNonEmpty(p.Egress.NoProxy, d.NoProxy),
		Egress:  p.EgressEnabled(),
	}
	if err := ValidateProxy(rt.Proxy, rt.Egress); err != nil {
		return Runtime{}, err
	}
	if err := ValidateImageSpec(p.Image); err != nil {
		return Runtime{}, err
	}
	if err := ValidateSrcs(p.Srcs); err != nil {
		return Runtime{}, err
	}

	domains := f.Domains
	if domains == "" {
		domains = strings.Join(p.Egress.AllowedDomains, ",")
	}
	if domains == "" {
		domains = baseDomains
	}
	rt.Domains = splitCSV(domains)
	if err := ValidateDomains(rt.Domains); err != nil {
		return Runtime{}, err
	}

	// Routes only take effect with the allowlist on; validate them there (they
	// are otherwise ignored in direct mode, with a CLI warning).
	rt.Routes = p.Egress.Routes
	if rt.Egress {
		if err := ValidateRoutes(rt.Domains, rt.Routes, rt.Egress); err != nil {
			return Runtime{}, err
		}
	}

	rt.Backend = firstNonEmpty(f.Backend, p.Backend, d.Backend)
	// Session deviates from the others: the env (d.Session) sits ABOVE the
	// profile, because SANDBOXER_SESSION=ephemeral is an operator kill-switch
	// that must win over a profile's `session:` choice.
	rt.Session = firstNonEmpty(f.Session, d.Session, p.Session, SessionPersistent)
	// Same shape for the restore's agent auto-resume: the env kill-switch
	// (SANDBOXER_NO_RESUME=1) outranks the profile's autoResume.
	rt.AutoResume = !d.NoResume && p.AutoResumeEnabled()

	// Resource caps: a profile's limits: overrides the SANDBOXER_MEM/SANDBOXER_CPU
	// env defaults; pids has no env default and comes straight from the profile.
	rt.Mem = firstNonEmpty(p.Limits.Memory, d.Mem)
	rt.CPU = firstNonEmpty(p.Limits.CPUs, d.CPU)
	rt.Pids = p.Limits.Pids
	return rt, nil
}

// DomainsCSV joins the resolved allowlist back to a comma-separated string.
func (r Runtime) DomainsCSV() string { return strings.Join(r.Domains, ",") }

// hostnameRe matches a plain DNS hostname: dot-separated labels of ASCII
// letters, digits and internal dashes, with an OPTIONAL leading dot (the
// subdomain-matching form squid's dstdomain uses). It forbids whitespace,
// control characters, '_', '/', a scheme prefix and a bare/only-dot value —
// every shape that could otherwise reach the generated squid.conf verbatim and
// either inject a directive (a newline/tab) or match every host (a lone ".").
var hostnameRe = regexp.MustCompile(`^\.?([A-Za-z0-9](-*[A-Za-z0-9])*\.)+[A-Za-z0-9](-*[A-Za-z0-9])*$`)

// ValidateDomains checks each domain for the common typos (each with a specific
// hint) and then against the strict hostname grammar. It returns nil for an
// empty list and the first invalid domain found.
//
// The strict check is a SECURITY boundary, not cosmetics: an allowlist domain
// is written verbatim into the egress squid.conf (egress.squidConf), above the
// `http_access deny all` line — so a value carrying a newline or tab could
// inject an `http_access allow all` directive and silently open all egress
// while the banner still reports the allowlist as on.
func ValidateDomains(domains []string) error {
	for _, d := range domains {
		if err := validateDomain(d); err != nil {
			return err
		}
	}
	return nil
}

// validateDomain validates one egress allowlist / route domain; a blank entry
// is skipped (nil). Shared by ValidateDomains and ValidateRoutes so an allowlist
// domain and a routed domain are held to the same grammar.
func validateDomain(d string) error {
	d = strings.TrimSpace(d)
	if d == "" {
		return nil
	}
	if strings.ContainsAny(d, " \t\r\n\v\f") {
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
	if !hostnameRe.MatchString(d) {
		return fmt.Errorf("invalid domain %q — expected a hostname like api.example.com "+
			"(letters, digits, dashes and dots; an optional leading dot matches subdomains)", d)
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
				"works (the sidecar chains over http); set egress.enabled = false to use it as a direct https proxy", proxyURL)
		}
		return nil
	default:
		return fmt.Errorf("invalid proxy %q — expected an http://host:port URL", proxyURL)
	}
}

// ValidateRoutes checks the per-domain proxy routes against the allowlist. Each
// route needs at least one domain and a proxy that passes ValidateProxy (an
// https parent is rejected under egress, like the default proxy). Every routed
// domain must be covered by the allowlist — squid denies a domain before it ever
// reaches the route's cache_peer otherwise, so an uncovered domain is dead config
// — and a domain may route to only one proxy. Empty routes is valid (nil error).
func ValidateRoutes(allowed []string, routes []Route, egress bool) error {
	claimed := map[string]int{} // routed domain -> route index (catch a domain in two routes)
	for i, r := range routes {
		if len(r.Domains) == 0 {
			return fmt.Errorf("egress.routes[%d]: a route needs at least one domain", i)
		}
		if r.Proxy == "" {
			return fmt.Errorf("egress.routes[%d]: a route needs a proxy", i)
		}
		if err := ValidateProxy(r.Proxy, egress); err != nil {
			return fmt.Errorf("egress.routes[%d]: %w", i, err)
		}
		for _, d := range r.Domains {
			d = strings.TrimSpace(d)
			if d == "" {
				return fmt.Errorf("egress.routes[%d]: empty domain", i)
			}
			if err := validateDomain(d); err != nil {
				return fmt.Errorf("egress.routes[%d]: %w", i, err)
			}
			if prev, ok := claimed[d]; ok {
				return fmt.Errorf("egress.routes: domain %q is in routes %d and %d — a domain can route to only one proxy", d, prev, i)
			}
			claimed[d] = i
			if !domainCovered(d, allowed) {
				return fmt.Errorf("egress.routes[%d]: domain %q is not covered by egress.allowedDomains — "+
					"squid denies it before the route proxy; add it to allowedDomains", i, d)
			}
		}
	}
	return nil
}

// domainCovered reports whether domain d is permitted by the allowlist under the
// same leading-dot suffix matching squid uses (an entry a matches a and any x.a),
// mirroring squidConf's dstdomain semantics.
func domainCovered(d string, allowed []string) bool {
	d = strings.TrimPrefix(strings.TrimSpace(d), ".")
	for _, a := range allowed {
		a = strings.TrimPrefix(strings.TrimSpace(a), ".")
		if a == "" {
			continue
		}
		if d == a || strings.HasSuffix(d, "."+a) {
			return true
		}
	}
	return false
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
