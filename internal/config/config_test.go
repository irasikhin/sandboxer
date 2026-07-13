package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestConfigPath(t *testing.T) {
	// The project profile lives under the state dir: .sandboxer/config.yaml.
	if got, want := ConfigPath(), filepath.Join(StateDirName, ConfigFileName); got != want {
		t.Errorf("ConfigPath() = %q, want %q", got, want)
	}
	if ConfigFileName != "config.yaml" || StateDirName != ".sandboxer" {
		t.Errorf("unexpected names: dir=%q file=%q", StateDirName, ConfigFileName)
	}
}

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"feature-x":    "feature-x",
		"feat/branch":  "feat-branch",
		"  spaces  ":   "spaces",
		"a@@b##c":      "a-b-c",
		"--leading--":  "leading",
		"under_score.": "under_score.",
	}
	for in, want := range cases {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveRuntimePrecedence(t *testing.T) {
	p := &Profile{
		Backend: "podman",
		Network: Network{AllowedDomains: []string{"x.com", "y.com"}, Proxy: "http://p"},
	}
	d := Defaults{Backend: "docker"}

	// Flag override beats profile; profile beats base/defaults.
	rt, err := ResolveRuntime(p, d, "base.com", Overrides{Backend: "flag-engine"})
	if err != nil {
		t.Fatal(err)
	}
	if rt.Backend != "flag-engine" {
		t.Errorf("backend: flag should win over profile, got %q", rt.Backend)
	}
	if !slices.Equal(rt.Domains, []string{"x.com", "y.com"}) {
		t.Errorf("domains: profile should win, got %v", rt.Domains)
	}
	if rt.Proxy != "http://p" {
		t.Errorf("proxy not carried: %q", rt.Proxy)
	}
	if !rt.Egress {
		t.Error("egress should default true")
	}

	// Nil profile, no overrides → defaults + base domains.
	rt2, err := ResolveRuntime(nil, d, "base.com,two.com", Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if rt2.Backend != "docker" {
		t.Errorf("backend default = %q", rt2.Backend)
	}
	if !slices.Equal(rt2.Domains, []string{"base.com", "two.com"}) {
		t.Errorf("base domains = %v", rt2.Domains)
	}
}

// TestResolveRuntimeLimits pins the resource-cap resolution: a profile's
// limits: overrides the SANDBOXER_MEM/SANDBOXER_CPU env defaults, memory/cpus
// fall back to those defaults when the profile is silent, and pids comes
// straight from the profile (no env default).
func TestResolveRuntimeLimits(t *testing.T) {
	// Profile limits win over the env defaults; pids is profile-only.
	p := &Profile{Limits: Limits{Memory: "4G", CPUs: "2", Pids: 512}}
	rt, err := ResolveRuntime(p, Defaults{Mem: "1G", CPU: "1"}, "base.com", Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if rt.Mem != "4G" || rt.CPU != "2" || rt.Pids != 512 {
		t.Errorf("profile limits should win: mem=%q cpu=%q pids=%d", rt.Mem, rt.CPU, rt.Pids)
	}

	// No profile limits → the env defaults apply, pids stays uncapped.
	rt2, err := ResolveRuntime(&Profile{}, Defaults{Mem: "1G", CPU: "1"}, "base.com", Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if rt2.Mem != "1G" || rt2.CPU != "1" || rt2.Pids != 0 {
		t.Errorf("env limits should apply: mem=%q cpu=%q pids=%d", rt2.Mem, rt2.CPU, rt2.Pids)
	}
}

// TestAnnotateRemovedKeys: a config that still uses a retired key trips the
// strict decoder, and annotateRemovedKeys upgrades the terse "field X not found"
// into a migration hint carrying the key and its guidance — on both the flat
// (Load) and document (LoadDocument) decode paths. Table-driven over the whole
// removedKeys table so a newly-retired key is covered automatically.
func TestAnnotateRemovedKeys(t *testing.T) {
	if len(removedKeys) == 0 {
		t.Skip("no removed keys yet")
	}
	for key, hint := range removedKeys {
		t.Run(key, func(t *testing.T) {
			dir := t.TempDir()
			flat := writeFile(t, dir, "flat.yaml", key+": whatever\n")
			_, err := Load(flat)
			if err == nil {
				t.Fatalf("a removed key %q must still be rejected", key)
			}
			for _, want := range []string{key, hint} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Load error %q missing %q", err, want)
				}
			}
			doc := writeFile(t, dir, "doc.yaml", "profiles:\n  web:\n    "+key+": whatever\n")
			if _, err := LoadDocument(doc); err == nil || !strings.Contains(err.Error(), hint) {
				t.Errorf("LoadDocument for removed key %q = %v, want the migration hint", key, err)
			}
		})
	}
}

// TestValidateRoutes pins the per-domain route validation: a route needs a
// domain and a proxy, every routed domain must be covered by the allowlist
// (leading-dot suffix matching), an https parent is rejected under egress, and a
// domain may route to only one proxy.
func TestValidateRoutes(t *testing.T) {
	allowed := []string{"api.anthropic.com", "github.com"}
	cases := []struct {
		name   string
		routes []Route
		egress bool
		ok     bool
	}{
		{"nil is fine", nil, true, true},
		{"valid covered route", []Route{{Domains: []string{"api.anthropic.com"}, Proxy: "http://bypass:8080"}}, true, true},
		{"subdomain covered by allowlist entry", []Route{{Domains: []string{"api.anthropic.com"}, Proxy: "http://p:1"}}, true, true},
		{"no domains", []Route{{Proxy: "http://p:1"}}, true, false},
		{"no proxy", []Route{{Domains: []string{"github.com"}}}, true, false},
		{"uncovered domain", []Route{{Domains: []string{"evil.com"}, Proxy: "http://p:1"}}, true, false},
		{"https parent under egress rejected", []Route{{Domains: []string{"github.com"}, Proxy: "https://p:1"}}, true, false},
		{"https parent ok with egress off", []Route{{Domains: []string{"github.com"}, Proxy: "https://p:1"}}, false, true},
		{"domain in two routes", []Route{
			{Domains: []string{"github.com"}, Proxy: "http://p:1"},
			{Domains: []string{"github.com"}, Proxy: "http://q:2"},
		}, true, false},
	}
	for _, c := range cases {
		err := ValidateRoutes(allowed, c.routes, c.egress)
		if (err == nil) != c.ok {
			t.Errorf("%s: ValidateRoutes err=%v, want ok=%v", c.name, err, c.ok)
		}
	}

	// domainCovered mirrors squid's leading-dot suffix match.
	if !domainCovered("api.anthropic.com", []string{"anthropic.com"}) {
		t.Error("an allowlist entry should cover its subdomains")
	}
	if domainCovered("notanthropic.com", []string{"anthropic.com"}) {
		t.Error("a non-suffix domain must not be covered")
	}
}

// TestResolveRuntimeRoutes: routes are validated (and rejected) only with egress
// on; with egress off they are carried but not validated (ignored at run time).
func TestResolveRuntimeRoutes(t *testing.T) {
	bad := &Profile{Network: Network{
		AllowedDomains: []string{"github.com"},
		Routes:         []Route{{Domains: []string{"evil.com"}, Proxy: "http://p:1"}},
	}}
	if _, err := ResolveRuntime(bad, Defaults{}, "", Overrides{}); err == nil {
		t.Error("a route domain outside the allowlist must fail under egress on")
	}
	off := false
	bad.Egress = &off
	if _, err := ResolveRuntime(bad, Defaults{}, "", Overrides{}); err != nil {
		t.Errorf("routes must not be validated with egress off: %v", err)
	}
}

func TestValidateProxy(t *testing.T) {
	cases := []struct {
		name   string
		url    string
		egress bool
		ok     bool
	}{
		{"empty", "", true, true},
		{"valid http, egress on", "http://host.docker.internal:3128", true, true},
		{"valid http, egress off", "http://host.docker.internal:3128", false, true},
		{"https with egress on rejected", "https://p:3128", true, false},
		{"https with egress off ok", "https://p:3128", false, true},
		{"scheme-less rejected", "p:3128", true, false},
		{"hostless rejected", "http://", true, false},
		{"unparseable rejected", "http://%zz", true, false},
	}
	for _, c := range cases {
		err := ValidateProxy(c.url, c.egress)
		if (err == nil) != c.ok {
			t.Errorf("%s: ValidateProxy err=%v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestResolveRuntimeProxy(t *testing.T) {
	// A single proxy URL is carried into Runtime and keeps egress on (chained).
	p := &Profile{Network: Network{Proxy: "http://host.docker.internal:3128"}}
	rt, err := ResolveRuntime(p, Defaults{}, "base.com", Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if rt.Proxy != "http://host.docker.internal:3128" {
		t.Errorf("proxy not carried into Runtime: %q", rt.Proxy)
	}
	if !rt.Egress {
		t.Error("a proxy with egress on must keep the allowlist on (chained mode)")
	}

	// SANDBOXER_PROXY (Defaults.Proxy) is the lowest-precedence fallback.
	rt2, err := ResolveRuntime(&Profile{}, Defaults{Proxy: "http://env:9999"}, "base.com", Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if rt2.Proxy != "http://env:9999" {
		t.Errorf("env proxy default not applied: %q", rt2.Proxy)
	}
	// A profile proxy beats the env default.
	rt3, err := ResolveRuntime(&Profile{Network: Network{Proxy: "http://prof:1"}}, Defaults{Proxy: "http://env:9999"}, "base.com", Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if rt3.Proxy != "http://prof:1" {
		t.Errorf("profile proxy should beat env default: %q", rt3.Proxy)
	}

	// https + egress on is rejected at resolve time.
	bad := &Profile{Network: Network{Proxy: "https://p:3128"}}
	if _, err := ResolveRuntime(bad, Defaults{}, "base.com", Overrides{}); err == nil {
		t.Error("ResolveRuntime should reject an https proxy with egress on")
	}
}

func TestEgressDisabled(t *testing.T) {
	no := false
	p := &Profile{Egress: &no}
	if p.EgressEnabled() {
		t.Error("egress: false should disable")
	}
	if !(&Profile{}).EgressEnabled() {
		t.Error("egress: default should be enabled")
	}
}

func TestLoadAndJSONRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	yaml := `name: feature-x
backend: podman
network:
  allowedDomains: [api.anthropic.com, registry.npmjs.org]
deps:
  - shared-lib
  - src/lib/util.go
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "feature-x" || p.Backend != "podman" {
		t.Errorf("scalars wrong: %+v", p)
	}
	if len(p.Network.AllowedDomains) != 2 {
		t.Errorf("domains: %v", p.Network.AllowedDomains)
	}
	if len(p.Deps) != 2 || p.Deps[1] != "src/lib/util.go" {
		t.Errorf("deps: %v", p.Deps)
	}
	// JSON serialization uses camelCase keys the container and sandbox read.
	b, err := p.JSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"deps"`, `"allowedDomains"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("JSON missing %s:\n%s", want, b)
		}
	}
}

func TestExampleProfilesParse(t *testing.T) {
	// The shipped examples must stay valid under the strict (KnownFields) schema.
	for _, name := range []string{"config.yaml", "with-deps.yaml", "profiles/web.yaml", "profiles/api.yaml", "profiles/custom-image.yaml"} {
		path := filepath.Join("..", "..", "examples", name)
		if _, err := os.Stat(path); err != nil {
			t.Skipf("example %s not present", name)
		}
		if _, err := Load(path); err != nil {
			t.Errorf("example %s failed to load: %v", name, err)
		}
	}
}

func TestLoadUnknownFieldRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte("name: x\nbogusField: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("expected error on unknown field (KnownFields strict)")
	}
}
