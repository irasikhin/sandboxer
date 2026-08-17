package config

import (
	"slices"
	"strings"
	"testing"
)

// TestParsePorts pins the spec grammar — microsandbox's own, so the config and
// the runtime never disagree about which side of a colon is the host.
func TestParsePorts(t *testing.T) {
	tests := []struct {
		name  string
		specs []string
		want  []Port
	}{
		{
			name:  "bare port publishes the same number on loopback",
			specs: []string{"3080"},
			want:  []Port{{Bind: "127.0.0.1", Host: 3080, Guest: 3080, Proto: "tcp"}},
		},
		{
			name:  "host:guest",
			specs: []string{"8080:3080"},
			want:  []Port{{Bind: "127.0.0.1", Host: 8080, Guest: 3080, Proto: "tcp"}},
		},
		{
			name:  "explicit bind",
			specs: []string{"0.0.0.0:8080:3080"},
			want:  []Port{{Bind: "0.0.0.0", Host: 8080, Guest: 3080, Proto: "tcp"}},
		},
		{
			name:  "bracketed ipv6 bind",
			specs: []string{"[::1]:8080:3080"},
			want:  []Port{{Bind: "::1", Host: 8080, Guest: 3080, Proto: "tcp"}},
		},
		{
			name:  "udp suffix",
			specs: []string{"5353:53/udp"},
			want:  []Port{{Bind: "127.0.0.1", Host: 5353, Guest: 53, Proto: "udp"}},
		},
		{
			name:  "blanks are skipped, whitespace trimmed",
			specs: []string{" ", " 3080 ", ""},
			want:  []Port{{Bind: "127.0.0.1", Host: 3080, Guest: 3080, Proto: "tcp"}},
		},
		{
			// Same host port on two DIFFERENT bind addresses is two listeners,
			// not a clash — only bind+port+proto identifies a forward.
			name:  "same port on different binds",
			specs: []string{"8080:3080", "0.0.0.0:8080:3081"},
			want: []Port{
				{Bind: "127.0.0.1", Host: 8080, Guest: 3080, Proto: "tcp"},
				{Bind: "0.0.0.0", Host: 8080, Guest: 3081, Proto: "tcp"},
			},
		},
		{
			name:  "none",
			specs: nil,
			want:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePorts(tt.specs)
			if err != nil {
				t.Fatalf("ParsePorts(%q): %v", tt.specs, err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("ParsePorts(%q) = %+v, want %+v", tt.specs, got, tt.want)
			}
		})
	}
}

// TestParsePortsErrors pins the refusals: every one of them is a spec that
// would otherwise reach msb and fail after the machine is half built, or —
// worse — publish something other than what was written.
func TestParsePortsErrors(t *testing.T) {
	tests := []struct {
		name  string
		specs []string
		want  string
	}{
		{name: "not a number", specs: []string{"http:3080"}, want: "is not a number"},
		{name: "zero", specs: []string{"0:3080"}, want: "out of range"},
		{name: "too large", specs: []string{"70000"}, want: "out of range"},
		{name: "negative", specs: []string{"-1:3080"}, want: "out of range"},
		{name: "unknown proto", specs: []string{"3080/sctp"}, want: "unknown protocol"},
		{name: "bad bind", specs: []string{"nothost:8080:3080"}, want: "not an IP"},
		{name: "too many parts", specs: []string{"1:2:3:4"}, want: "too many colon-separated parts"},
		{name: "unterminated ipv6", specs: []string{"[::1:8080:3080"}, want: "unterminated"},
		{name: "ipv6 with extra parts", specs: []string{"[::1]:1:2:3"}, want: "too many parts after the bind"},
		{name: "duplicate forward", specs: []string{"8080:3080", "8080:3081"}, want: "both publish"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParsePorts(tt.specs)
			if err == nil {
				t.Fatalf("ParsePorts(%q) = nil error, want %q", tt.specs, tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("ParsePorts(%q) error = %v, want it to mention %q", tt.specs, err, tt.want)
			}
		})
	}
}

// TestPortRendering pins the two argv shapes a published port turns into (the
// runner's forward and the policy door it needs) plus the banner text.
func TestPortRendering(t *testing.T) {
	tests := []struct {
		port                     Port
		publish, rule, str, name string
		public                   bool
	}{
		{
			name:    "tcp loopback",
			port:    Port{Bind: "127.0.0.1", Host: 8080, Guest: 3080, Proto: "tcp"},
			publish: "127.0.0.1:8080:3080",
			rule:    "allow:ingress@0.0.0.0/0:tcp:3080",
			str:     "127.0.0.1:8080→3080/tcp",
		},
		{
			name:    "udp on every interface",
			port:    Port{Bind: "0.0.0.0", Host: 5353, Guest: 53, Proto: "udp"},
			publish: "0.0.0.0:5353:53/udp",
			rule:    "allow:ingress@0.0.0.0/0:udp:53",
			str:     "0.0.0.0:5353→53/udp",
			public:  true,
		},
		{
			name:    "ipv6 bind is bracketed",
			port:    Port{Bind: "::1", Host: 8080, Guest: 3080, Proto: "tcp"},
			publish: "[::1]:8080:3080",
			rule:    "allow:ingress@0.0.0.0/0:tcp:3080",
			str:     "[::1]:8080→3080/tcp",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.port.Publish(); got != tt.publish {
				t.Errorf("Publish() = %q, want %q", got, tt.publish)
			}
			if got := tt.port.IngressRule(); got != tt.rule {
				t.Errorf("IngressRule() = %q, want %q", got, tt.rule)
			}
			if got := tt.port.String(); got != tt.str {
				t.Errorf("String() = %q, want %q", got, tt.str)
			}
			if got := tt.port.Public(); got != tt.public {
				t.Errorf("Public() = %v, want %v", got, tt.public)
			}
		})
	}
}

// TestResolveRuntimePorts pins the resolution chain: the profile publishes, the
// flag REPLACES the profile's list wholesale (like --allow-domains), and the
// env kill-switch closes every forward whatever either says.
func TestResolveRuntimePorts(t *testing.T) {
	cases := []struct {
		name    string
		profile []string
		flag    []string
		noPorts bool
		want    []Port
	}{
		{name: "none by default"},
		{
			name:    "from the profile",
			profile: []string{"3080"},
			want:    []Port{{Bind: "127.0.0.1", Host: 3080, Guest: 3080, Proto: "tcp"}},
		},
		{
			name:    "flag replaces the profile",
			profile: []string{"3080", "9229"},
			flag:    []string{"8080:3080"},
			want:    []Port{{Bind: "127.0.0.1", Host: 8080, Guest: 3080, Proto: "tcp"}},
		},
		{
			// An empty --port cannot be spelled (the flag is repeatable, so
			// "unset" is nil), which is why the kill-switch exists.
			name:    "env kills the profile's ports",
			profile: []string{"3080"},
			noPorts: true,
		},
		{
			name:    "env kills the flag's ports",
			flag:    []string{"3080"},
			noPorts: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rt, err := ResolveRuntime(&Profile{Ports: c.profile}, Defaults{NoPorts: c.noPorts}, "",
				Overrides{Ports: c.flag})
			if err != nil {
				t.Fatalf("ResolveRuntime: %v", err)
			}
			if !slices.Equal(rt.Ports, c.want) {
				t.Errorf("Ports = %+v, want %+v", rt.Ports, c.want)
			}
		})
	}
}

// TestResolveRuntimeRejectsBadPort: a malformed spec fails the whole
// resolution, so create refuses before it writes any state — a port silently
// dropped would read as a broken server inside the sandbox.
func TestResolveRuntimeRejectsBadPort(t *testing.T) {
	if _, err := ResolveRuntime(&Profile{Ports: []string{"3080:oops"}}, Defaults{}, "", Overrides{}); err == nil {
		t.Fatal("expected an error for a malformed port spec")
	}
	// ...but not when the operator switched forwards off: nothing is published,
	// so nothing is parsed.
	if _, err := ResolveRuntime(&Profile{Ports: []string{"3080:oops"}}, Defaults{NoPorts: true}, "", Overrides{}); err != nil {
		t.Fatalf("NoPorts must skip parsing entirely: %v", err)
	}
}

// TestLoadDefaultsNoPorts: the env kill-switch is read strictly as "1".
func TestLoadDefaultsNoPorts(t *testing.T) {
	t.Setenv("SANDBOXER_NO_PORTS", "1")
	if d := LoadDefaults(); !d.NoPorts {
		t.Error("SANDBOXER_NO_PORTS=1 must set NoPorts")
	}
	t.Setenv("SANDBOXER_NO_PORTS", "yes")
	if d := LoadDefaults(); d.NoPorts {
		t.Error("only SANDBOXER_NO_PORTS=1 disables ports")
	}
}
