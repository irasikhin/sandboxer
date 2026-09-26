package config

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// HostAlias is microsandbox's magic hostname for the HOST: its DNS forwarder
// synthesizes the sandbox's gateway IP for it, and a gateway-bound dial is
// rewritten to the host loopback at connect time (msb HOST_ALIAS +
// resolve_host_dst) — msb's host.docker.internal. It is the only way a guest
// reaches a service bound to the host's 127.0.0.1: the guest's own loopback is
// its smoltcp stack, so dialing 127.0.0.1 in-guest never leaves the VM. Kept
// here (not in the backend) because the CLI's banner names the same address.
const HostAlias = "host.microsandbox.internal"

// HostPort is ONE port (or contiguous range) on the HOST the sandbox may
// reach. The guest dials it at host.microsandbox.internal:<port> (see
// HostAlias) — the guest's own 127.0.0.1 is the VM itself, so a host service
// is only ever reachable through this alias plus an explicit policy door.
type HostPort struct {
	Lo    int    // first port of the range
	Hi    int    // == Lo for a single port
	Proto string // "tcp" or "udp"
}

// Rule renders the host port as microsandbox's policy rule — the ONE door that
// admits the guest's dial while the machine's default posture stands:
// `allow@host:<proto>:<port>` (`host` is the destination group a gateway-bound
// dial to host.microsandbox.internal classifies as).
func (h HostPort) Rule() string {
	return fmt.Sprintf("allow@host:%s:%s", h.Proto, h.DialPort())
}

// DialPort renders the port, or LO-HI for a range, as it appears in a dial
// address.
func (h HostPort) DialPort() string {
	if h.Lo == h.Hi {
		return strconv.Itoa(h.Lo)
	}
	return fmt.Sprintf("%d-%d", h.Lo, h.Hi)
}

// Dial renders the in-sandbox address of the host port, ready to paste into an
// HTTP_PROXY URL: host.microsandbox.internal:7890.
func (h HostPort) Dial() string { return HostAlias + ":" + h.DialPort() }

// String renders the host port for banners and diagnostics: 7890/tcp or
// 7890-7899/tcp.
func (h HostPort) String() string {
	return fmt.Sprintf("%s/%s", h.DialPort(), h.Proto)
}

// ParseHostPorts turns the profile's `hostPorts` specs into resolved doors.
// The grammar is deliberately small — one port or one contiguous range, and
// the protocol:
//
//	7890          → tcp, the single port 7890
//	7890/tcp      → the same, spelled out
//	7890-7899     → tcp, the contiguous range
//	5353/udp      → udp, the single port 5353
//
// Blank entries are skipped (as in the allowlist); everything else must parse,
// because a door silently dropped reads as "sandboxer opened it and the
// service is broken". Exact duplicates are dropped and the result is SORTED —
// the rules end up in the create argv, and a stable order keeps the session
// hash stable (the same convention msbNetTargets follows).
func ParseHostPorts(specs []string) ([]HostPort, error) {
	var out []HostPort
	seen := map[HostPort]bool{}
	for _, spec := range specs {
		s := strings.TrimSpace(spec)
		if s == "" {
			continue
		}
		h, err := parseHostPort(s)
		if err != nil {
			return nil, err
		}
		if seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Proto != out[j].Proto {
			return out[i].Proto < out[j].Proto
		}
		if out[i].Lo != out[j].Lo {
			return out[i].Lo < out[j].Lo
		}
		return out[i].Hi < out[j].Hi
	})
	return out, nil
}

// parseHostPort parses one spec. The error names the offending spec and the
// whole grammar: PORT and LO-HI are close enough that "invalid host port"
// alone would leave a user guessing which half sandboxer disliked.
func parseHostPort(spec string) (HostPort, error) {
	s, proto := spec, "tcp"
	if i := strings.LastIndex(spec, "/"); i >= 0 {
		proto, s = strings.ToLower(spec[i+1:]), spec[:i]
		switch proto {
		case "":
			return HostPort{}, hostPortErr(spec, "empty protocol")
		case "tcp", "udp":
		default:
			return HostPort{}, hostPortErr(spec, fmt.Sprintf("unknown protocol %q — tcp or udp", proto))
		}
	}
	lo, hi := s, s
	if i := strings.Index(s, "-"); i >= 0 {
		lo, hi = s[:i], s[i+1:]
	}
	if lo == "" || hi == "" {
		return HostPort{}, hostPortErr(spec, "missing port in the range")
	}
	loN, err := hostPortNumber(spec, lo)
	if err != nil {
		return HostPort{}, err
	}
	hiN := loN
	if hi != lo {
		if hiN, err = hostPortNumber(spec, hi); err != nil {
			return HostPort{}, err
		}
	}
	if loN > hiN {
		return HostPort{}, hostPortErr(spec, fmt.Sprintf("range %d-%d is reversed — LO must be <= HI", loN, hiN))
	}
	return HostPort{Lo: loN, Hi: hiN, Proto: proto}, nil
}

// hostPortNumber parses one port field, rejecting anything outside the TCP/UDP
// range — a host door names a real port, and 0 would open nothing (or, on the
// guest's side, the whole ephemeral space, depending on the engine's reading).
func hostPortNumber(spec, field string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(field))
	if err != nil {
		return 0, hostPortErr(spec, fmt.Sprintf("port %q is not a number", field))
	}
	if n < 1 || n > 65535 {
		return 0, hostPortErr(spec, fmt.Sprintf("port %d is out of range (1-65535)", n))
	}
	return n, nil
}

func hostPortErr(spec, why string) error {
	return fmt.Errorf("invalid host port %q — %s; expected PORT[/tcp|udp] or LO-HI[/tcp|udp], "+
		"e.g. 7890, 7890-7899, 5353/udp", spec, why)
}
