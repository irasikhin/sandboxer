package config

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// DefaultPortBind is the host address a published port binds to when the spec
// names none: the loopback interface, so a forwarded port reaches the machine's
// own browser and nothing on the network. Publishing on 0.0.0.0 is possible but
// must be spelled out (and the banner says so — cli.warnPublicPorts).
const DefaultPortBind = "127.0.0.1"

// Port is one published port: a host-side listener the microVM forwards to a
// guest port, so a server started INSIDE the sandbox (a dev server, dsh's
// browser UI) is reachable from the host.
type Port struct {
	// Bind is the host address the forward listens on (never empty after
	// ParsePorts — an unspecified bind resolves to DefaultPortBind).
	Bind  string
	Host  int
	Guest int
	// Proto is "tcp" or "udp" (the /udp spec suffix).
	Proto string
}

// ParsePorts turns the profile's `ports` specs into resolved forwards. The
// grammar is microsandbox's own, so the config and the runtime never disagree
// about which side is which:
//
//	3080                  → 127.0.0.1:3080 → guest 3080
//	8080:3080             → 127.0.0.1:8080 → guest 3080   (HOST:GUEST)
//	0.0.0.0:8080:3080     → every interface (BIND:HOST:GUEST)
//	[::1]:8080:3080       → an IPv6 bind, bracketed
//	5353:53/udp           → the same, over UDP
//
// Blank entries are skipped (as in the allowlist); everything else must parse,
// because a port silently dropped reads as "sandboxer published it and the
// server is broken". Two forwards claiming one host address+port are rejected
// here rather than at the runtime's bind, which fails after the machine is
// half-built.
func ParsePorts(specs []string) ([]Port, error) {
	var out []Port
	seen := map[string]string{}
	for _, spec := range specs {
		s := strings.TrimSpace(spec)
		if s == "" {
			continue
		}
		p, err := parsePort(s)
		if err != nil {
			return nil, err
		}
		key := fmt.Sprintf("%s/%s:%d", p.Proto, p.Bind, p.Host)
		if prev, dup := seen[key]; dup {
			return nil, fmt.Errorf("ports %q and %q both publish %s:%d/%s — one host port, one forward",
				prev, spec, p.Bind, p.Host, p.Proto)
		}
		seen[key] = spec
		out = append(out, p)
	}
	return out, nil
}

// parsePort parses one spec. The error names the offending spec and the whole
// grammar: the shapes are close enough that "invalid port" alone would leave a
// user guessing which half sandboxer disliked.
func parsePort(spec string) (Port, error) {
	s, proto := spec, "tcp"
	if i := strings.LastIndex(s, "/"); i >= 0 {
		proto, s = strings.ToLower(s[i+1:]), s[:i]
		if proto != "tcp" && proto != "udp" {
			return Port{}, portErr(spec, fmt.Sprintf("unknown protocol %q — tcp or udp", proto))
		}
	}
	bind := ""
	if strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end < 0 {
			return Port{}, portErr(spec, "unterminated [ipv6] bind address")
		}
		bind, s = s[1:end], strings.TrimPrefix(s[end+1:], ":")
	}
	fields := strings.Split(s, ":")
	if bind != "" && len(fields) > 2 {
		return Port{}, portErr(spec, "too many parts after the bind address")
	}
	switch {
	case bind == "" && len(fields) == 3:
		bind, fields = fields[0], fields[1:]
	case len(fields) > 3:
		return Port{}, portErr(spec, "too many colon-separated parts")
	}
	if bind == "" {
		bind = DefaultPortBind
	} else if net.ParseIP(bind) == nil {
		return Port{}, portErr(spec, fmt.Sprintf("bind address %q is not an IP", bind))
	}
	host, err := portNumber(spec, fields[0])
	if err != nil {
		return Port{}, err
	}
	guest := host
	if len(fields) == 2 {
		if guest, err = portNumber(spec, fields[1]); err != nil {
			return Port{}, err
		}
	}
	return Port{Bind: bind, Host: host, Guest: guest, Proto: proto}, nil
}

// portNumber parses one port field, rejecting 0 (msb would pick a random port,
// which no one can then dial) and anything outside the TCP/UDP range.
func portNumber(spec, field string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(field))
	if err != nil {
		return 0, portErr(spec, fmt.Sprintf("port %q is not a number", field))
	}
	if n < 1 || n > 65535 {
		return 0, portErr(spec, fmt.Sprintf("port %d is out of range (1-65535)", n))
	}
	return n, nil
}

func portErr(spec, why string) error {
	return fmt.Errorf("invalid port %q — %s; expected GUEST, HOST:GUEST or BIND:HOST:GUEST "+
		"(optionally /udp), e.g. 3080, 8080:3080, 127.0.0.1:8080:3080", spec, why)
}

// Publish renders the forward in microsandbox's `-p` grammar.
func (p Port) Publish() string {
	return fmt.Sprintf("%s:%d:%d%s", bindLiteral(p.Bind), p.Host, p.Guest, protoSuffix(p.Proto))
}

// IngressRule is the ONE policy rule that lets a forwarded connection reach the
// guest while the machine keeps its default-deny wall (--no-net). It is scoped
// to the published guest port and its protocol, but NOT to a source: verified
// against msb 0.6.7, an `allow:ingress@host`, `@private` or `@public` rule does
// not match a forwarded connection at all — only `0.0.0.0/0` does. The reach
// that opens is exactly one port of a machine whose only inbound path is the
// host-side forward this rule accompanies (see backend.msbNetworkArgs).
func (p Port) IngressRule() string {
	return fmt.Sprintf("allow:ingress@0.0.0.0/0:%s:%d", p.Proto, p.Guest)
}

// String renders the forward for banners and diagnostics.
func (p Port) String() string {
	return fmt.Sprintf("%s:%d→%d/%s", bindLiteral(p.Bind), p.Host, p.Guest, p.Proto)
}

// Public reports whether the forward listens beyond the host's loopback — the
// state worth a banner warning, since it puts the guest's port on the network.
func (p Port) Public() bool {
	ip := net.ParseIP(p.Bind)
	return ip != nil && !ip.IsLoopback()
}

// bindLiteral brackets an IPv6 address so `host:port` stays unambiguous.
func bindLiteral(bind string) string {
	if ip := net.ParseIP(bind); ip != nil && ip.To4() == nil {
		return "[" + bind + "]"
	}
	return bind
}

func protoSuffix(proto string) string {
	if proto == "udp" {
		return "/udp"
	}
	return ""
}
