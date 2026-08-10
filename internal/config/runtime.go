package config

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Runtime is the fully-resolved set of effective settings for one sandbox
// invocation (the bash load_runtime result). It carries no filesystem state.
type Runtime struct {
	// Proxy is the resolved proxy URL (empty = none). With a proxy set the
	// guest's HTTP(S) clients are pointed at it over an open VM network — the
	// proxy is the egress control point. A loopback host is adapted for the
	// guest at launch (see backend.msbGuestProxyURL).
	Proxy   string
	NoProxy string   // NO_PROXY, applied alongside Proxy
	Domains []string // resolved egress allowlist
	Backend string
	Session string // SessionPersistent or SessionEphemeral (resolved; never empty)
	Egress  bool
	// Resource caps sizing the machine. Mem/CPU resolve profile-over-env
	// (limits: over SANDBOXER_MEM/SANDBOXER_CPU); empty means the microVM
	// default size.
	Mem string
	CPU string
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
	if err := ValidateProxy(rt.Proxy); err != nil {
		return Runtime{}, err
	}
	if err := ValidateImageSpec(p.Image); err != nil {
		return Runtime{}, err
	}
	if err := ValidateSrcs(p.Srcs); err != nil {
		return Runtime{}, err
	}

	// Precedence flag > profile > base(run.env)/defaults, with one distinction
	// the plain firstNonEmpty pattern cannot make: an allowlist that is present
	// but EMPTY. `allowedDomains = [ ]` decodes to a non-nil empty slice while an
	// absent attr leaves it nil, and the two must not mean the same thing —
	// folding them together made "allow nothing" silently resolve to the full
	// built-in default set, i.e. the one spelling a reader is sure means
	// "deny everything" was the one that opened 40 domains. An explicit empty
	// list now stays empty; the backends have a defined answer for it (a
	// microVM boots with no route at all — a fully offline machine).
	var domains []string
	switch {
	case f.Domains != "":
		domains = splitCSV(f.Domains)
	case p.Egress.AllowedDomains != nil:
		domains = splitCSV(strings.Join(p.Egress.AllowedDomains, ","))
	default:
		domains = splitCSV(baseDomains)
	}
	rt.Domains = domains
	if err := ValidateDomains(rt.Domains); err != nil {
		return Runtime{}, err
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
	// env defaults.
	rt.Mem = firstNonEmpty(p.Limits.Memory, d.Mem)
	rt.CPU = firstNonEmpty(p.Limits.CPUs, d.CPU)
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

// ValidateBackend rejects an unsupported isolation backend. sandboxer runs
// every agent inside a real microVM — "microvm" (smolvm) or "microsandbox"
// (msb). The docker/podman container backend was removed, so a stale
// container-era value ("", "auto", "docker", "podman") gets a clear migration
// error instead of silently resolving to anything.
func ValidateBackend(rt Runtime) error {
	switch rt.Backend {
	case "microvm", "microsandbox":
		return nil
	case "", "auto", "docker", "podman":
		return fmt.Errorf("the docker/podman container backend was removed — set backend = \"microsandbox\" "+
			"(or \"microvm\"); a microVM runs container engines natively, so docker/podman still work "+
			"INSIDE the sandbox (got backend %q)", rt.Backend)
	case "native":
		return errors.New("the native backend was removed — use backend = \"microsandbox\" (or \"microvm\")")
	default:
		return fmt.Errorf("unknown backend %q — use microsandbox or microvm", rt.Backend)
	}
}

// IsMicrovmBackend reports whether b names a microVM backend — a real virtual
// machine per sandbox. Two runners sit on the same libkrun VMM and share every
// rule the CLI applies to microVMs: "microvm" (smolvm) and "microsandbox"
// (msb). Callers that must distinguish the RUNNER compare the backend string
// itself.
func IsMicrovmBackend(b string) bool { return b == "microvm" || b == "microsandbox" }

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
// http:// or https:// URL with a host — both schemes are fine in every egress
// state, since the guest talks to the proxy directly (there is no chaining
// sidecar). An empty proxy is valid (no proxy).
func ValidateProxy(proxyURL string) error {
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
	case "http", "https":
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
