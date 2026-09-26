package config

import (
	"slices"
	"strings"
	"testing"
)

// TestParseHostPorts pins the spec grammar: PORT[/tcp|udp] or
// LO-HI[/tcp|udp], blanks skipped, exact duplicates dropped, the result sorted
// so the emitted rules (and therefore the session hash) are stable.
func TestParseHostPorts(t *testing.T) {
	tests := []struct {
		name  string
		specs []string
		want  []HostPort
	}{
		{
			name:  "bare port defaults to tcp",
			specs: []string{"7890"},
			want:  []HostPort{{Lo: 7890, Hi: 7890, Proto: "tcp"}},
		},
		{
			name:  "explicit tcp",
			specs: []string{"7890/tcp"},
			want:  []HostPort{{Lo: 7890, Hi: 7890, Proto: "tcp"}},
		},
		{
			name:  "udp suffix",
			specs: []string{"5353/udp"},
			want:  []HostPort{{Lo: 5353, Hi: 5353, Proto: "udp"}},
		},
		{
			name:  "contiguous range",
			specs: []string{"7890-7899"},
			want:  []HostPort{{Lo: 7890, Hi: 7899, Proto: "tcp"}},
		},
		{
			name:  "range with protocol",
			specs: []string{"6000-6010/udp"},
			want:  []HostPort{{Lo: 6000, Hi: 6010, Proto: "udp"}},
		},
		{
			name:  "blanks are skipped, whitespace trimmed",
			specs: []string{" ", " 7890 ", ""},
			want:  []HostPort{{Lo: 7890, Hi: 7890, Proto: "tcp"}},
		},
		{
			// 7890 and 7890/tcp resolve to the SAME door; emitting the rule
			// twice would only make the argv (and the hash) noisier.
			name:  "exact duplicates dropped across spellings",
			specs: []string{"7890", "7890/tcp", "7890"},
			want:  []HostPort{{Lo: 7890, Hi: 7890, Proto: "tcp"}},
		},
		{
			name:  "sorted by protocol then port",
			specs: []string{"8080", "3080", "5353/udp", "7890-7899"},
			want: []HostPort{
				{Lo: 3080, Hi: 3080, Proto: "tcp"},
				{Lo: 7890, Hi: 7899, Proto: "tcp"},
				{Lo: 8080, Hi: 8080, Proto: "tcp"},
				{Lo: 5353, Hi: 5353, Proto: "udp"},
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
			got, err := ParseHostPorts(tt.specs)
			if err != nil {
				t.Fatalf("ParseHostPorts(%q): %v", tt.specs, err)
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("ParseHostPorts(%q) = %+v, want %+v", tt.specs, got, tt.want)
			}
		})
	}
}

// TestParseHostPortsErrors pins the refusals: each spec names the offending
// value and the whole grammar, because a door silently dropped reads as
// "sandboxer opened it and the service is broken".
func TestParseHostPortsErrors(t *testing.T) {
	tests := []struct {
		name  string
		specs []string
		want  string
	}{
		{name: "zero", specs: []string{"0"}, want: "out of range"},
		{name: "too large", specs: []string{"70000"}, want: "port 70000 is out of range (1-65535)"},
		{name: "reversed range", specs: []string{"9-8"}, want: "reversed"},
		{name: "not a number", specs: []string{"abc"}, want: "not a number"},
		{name: "unknown protocol", specs: []string{"7890/sctp"}, want: "unknown protocol"},
		{name: "empty protocol", specs: []string{"7890/"}, want: "empty protocol"},
		{name: "trailing dash", specs: []string{"7890-"}, want: "missing port"},
		{name: "leading dash", specs: []string{"-7890"}, want: "missing port"},
		{name: "unnumbered range end", specs: []string{"7890-abcd"}, want: "not a number"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseHostPorts(tt.specs)
			if err == nil {
				t.Fatalf("ParseHostPorts(%q) = nil error, want %q", tt.specs, tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("ParseHostPorts(%q) error = %v, want it to mention %q", tt.specs, err, tt.want)
			}
			// The message carries the spec and the grammar, so the fix is on
			// the same line as the complaint.
			for _, want := range []string{tt.specs[0], "expected PORT[/tcp|udp] or LO-HI[/tcp|udp]"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("ParseHostPorts(%q) error %q missing %q", tt.specs, err, want)
				}
			}
		})
	}
}

// TestHostPortRendering pins the three strings a host port turns into: the
// policy rule msb reads, the banner form, and the dial address printed to the
// user (the alias is the whole point — the guest's own 127.0.0.1 is the VM).
func TestHostPortRendering(t *testing.T) {
	tests := []struct {
		h                         HostPort
		rule, str, dialPort, dial string
	}{
		{
			h:    HostPort{Lo: 7890, Hi: 7890, Proto: "tcp"},
			rule: "allow@host:tcp:7890", str: "7890/tcp",
			dialPort: "7890", dial: "host.microsandbox.internal:7890",
		},
		{
			h:    HostPort{Lo: 7890, Hi: 7899, Proto: "tcp"},
			rule: "allow@host:tcp:7890-7899", str: "7890-7899/tcp",
			dialPort: "7890-7899", dial: "host.microsandbox.internal:7890-7899",
		},
		{
			h:    HostPort{Lo: 5353, Hi: 5353, Proto: "udp"},
			rule: "allow@host:udp:5353", str: "5353/udp",
			dialPort: "5353", dial: "host.microsandbox.internal:5353",
		},
	}
	for _, tt := range tests {
		if got := tt.h.Rule(); got != tt.rule {
			t.Errorf("%+v.Rule() = %q, want %q", tt.h, got, tt.rule)
		}
		if got := tt.h.String(); got != tt.str {
			t.Errorf("%+v.String() = %q, want %q", tt.h, got, tt.str)
		}
		if got := tt.h.DialPort(); got != tt.dialPort {
			t.Errorf("%+v.DialPort() = %q, want %q", tt.h, got, tt.dialPort)
		}
		if got := tt.h.Dial(); got != tt.dial {
			t.Errorf("%+v.Dial() = %q, want %q", tt.h, got, tt.dial)
		}
	}
}

// TestResolveRuntimeHostPorts pins the resolution chain: the profile opens
// doors, the flag REPLACES the profile's list wholesale (like --port), the env
// default is the lowest rung, and the kill-switch drops everything.
func TestResolveRuntimeHostPorts(t *testing.T) {
	cases := []struct {
		name        string
		profile     []string
		flag        []string
		env         string
		noHostPorts bool
		want        []HostPort
	}{
		{name: "none by default"},
		{
			name:    "from the profile",
			profile: []string{"7890"},
			want:    []HostPort{{Lo: 7890, Hi: 7890, Proto: "tcp"}},
		},
		{
			name:    "flag replaces the profile",
			profile: []string{"7890", "9229"},
			flag:    []string{"7890-7899"},
			want:    []HostPort{{Lo: 7890, Hi: 7899, Proto: "tcp"}},
		},
		{
			name:        "env kills the profile's host ports",
			profile:     []string{"7890"},
			noHostPorts: true,
		},
		{
			name:        "env kills the flag's host ports",
			flag:        []string{"7890"},
			noHostPorts: true,
		},
		{
			// SANDBOXER_HOST_PORTS is the "every sandbox can reach this"
			// knob: it applies when neither the flag nor the profile says
			// anything.
			name: "env default when nothing else opens a door",
			env:  "7890,5353/udp",
			want: []HostPort{
				{Lo: 7890, Hi: 7890, Proto: "tcp"},
				{Lo: 5353, Hi: 5353, Proto: "udp"},
			},
		},
		{
			name:    "profile outranks the env default",
			env:     "7890",
			profile: []string{"9229"},
			want:    []HostPort{{Lo: 9229, Hi: 9229, Proto: "tcp"}},
		},
		{
			// The allowlist's present-but-empty distinction: `hostPorts = [ ]`
			// is a deliberate "no doors", not a fall-through to the env.
			name:    "explicitly empty profile host ports beat the env default",
			env:     "7890",
			profile: []string{},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rt, err := ResolveRuntime(&Profile{HostPorts: c.profile},
				Defaults{NoHostPorts: c.noHostPorts, HostPorts: c.env}, "", Overrides{HostPorts: c.flag})
			if err != nil {
				t.Fatalf("ResolveRuntime: %v", err)
			}
			if !slices.Equal(rt.HostPorts, c.want) {
				t.Errorf("HostPorts = %+v, want %+v", rt.HostPorts, c.want)
			}
		})
	}
}

// TestResolveRuntimeRejectsBadHostPort: a malformed spec fails the whole
// resolution, so create refuses before it writes any state — a door silently
// dropped would read as a service that is somehow broken.
func TestResolveRuntimeRejectsBadHostPort(t *testing.T) {
	if _, err := ResolveRuntime(&Profile{HostPorts: []string{"70000"}}, Defaults{}, "", Overrides{}); err == nil {
		t.Fatal("expected an error for a malformed host port spec")
	}
	// ...but not when the operator switched host doors off: nothing is opened,
	// so nothing is parsed.
	if _, err := ResolveRuntime(&Profile{HostPorts: []string{"70000"}}, Defaults{NoHostPorts: true}, "", Overrides{}); err != nil {
		t.Fatalf("NoHostPorts must skip parsing entirely: %v", err)
	}
}

// TestRetiredProxyEnvFailsResolve: the removal of the automatic proxy wiring is
// a hard error, never a silent ignore — booting without the proxy env while the
// user asked for one would send the guest's traffic directly to the network.
func TestRetiredProxyEnvFailsResolve(t *testing.T) {
	t.Setenv("SANDBOXER_PROXY", "http://proxy.corp:3128")
	if _, err := ResolveRuntime(&Profile{}, Defaults{}, "", Overrides{}); err == nil {
		t.Fatal("SANDBOXER_PROXY must fail resolve")
	} else {
		for _, want := range []string{"SANDBOXER_PROXY is retired", "hostPorts", "HTTP(S)_PROXY", HostAlias} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("SANDBOXER_PROXY error %q missing %q", err, want)
			}
		}
	}

	t.Setenv("SANDBOXER_PROXY", "")
	t.Setenv("SANDBOXER_NO_PROXY", "localhost,127.0.0.1")
	if _, err := ResolveRuntime(&Profile{}, Defaults{}, "", Overrides{}); err == nil {
		t.Fatal("SANDBOXER_NO_PROXY must fail resolve")
	} else {
		for _, want := range []string{"SANDBOXER_NO_PROXY is retired", "NO_PROXY in env yourself"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("SANDBOXER_NO_PROXY error %q missing %q", err, want)
			}
		}
	}
}

// TestRemovedProxyKeysHint: the retired proxy keys sit in the removal table, so
// a config that still carries them fails the strict decode with the migration
// hint — for the old top-level spelling AND the newer egress.proxy one (the
// strict decoder reports both as an unknown field "proxy").
func TestRemovedProxyKeysHint(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		key, body, want string
	}{
		{"proxy", `{ proxy = "http://p:3128"; }`, "hostPorts"},
		{"noProxy", `{ noProxy = "localhost"; }`, "env.NO_PROXY"},
		{"egress.proxy", `{ egress.proxy = "http://p:3128"; }`, "hostPorts"},
		{"egress.noProxy", `{ egress.noProxy = "localhost"; }`, "env.NO_PROXY"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			path := writeFile(t, dir, strings.ReplaceAll(tc.key, ".", "-")+".nix", tc.body+"\n")
			_, err := LoadDocument(path)
			if err == nil {
				t.Fatalf("%s must still be rejected", tc.key)
			}
			for _, want := range []string{"was removed", tc.want} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing %q", err, want)
				}
			}
		})
	}
}

// TestLoadDefaultsHostPorts: SANDBOXER_HOST_PORTS carries the every-sandbox
// host doors; SANDBOXER_NO_HOST_PORTS=1 is the operator kill-switch, read
// strictly as "1".
func TestLoadDefaultsHostPorts(t *testing.T) {
	t.Setenv("SANDBOXER_HOST_PORTS", "7890,5353/udp")
	if d := LoadDefaults(); d.HostPorts != "7890,5353/udp" {
		t.Errorf("Defaults.HostPorts = %q, want the SANDBOXER_HOST_PORTS value", d.HostPorts)
	}
	t.Setenv("SANDBOXER_NO_HOST_PORTS", "1")
	if d := LoadDefaults(); !d.NoHostPorts {
		t.Error("SANDBOXER_NO_HOST_PORTS=1 must set NoHostPorts")
	}
	t.Setenv("SANDBOXER_NO_HOST_PORTS", "yes")
	if d := LoadDefaults(); d.NoHostPorts {
		t.Error("only SANDBOXER_NO_HOST_PORTS=1 disables host ports")
	}
}
