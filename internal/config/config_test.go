package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

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
		Agent:   "opencode",
		Model:   "m1",
		Network: Network{AllowedDomains: []string{"x.com", "y.com"}},
		Proxy:   Proxy{HTTP: "http://p"},
	}
	d := Defaults{Agent: "claude", Backend: "podman"}

	// Flag override beats profile; profile beats base/defaults.
	rt := ResolveRuntime(p, d, "base.com", "bm", Overrides{Agent: "crush"})
	if rt.Agent != "crush" {
		t.Errorf("agent: flag should win, got %q", rt.Agent)
	}
	if rt.Model != "m1" {
		t.Errorf("model: profile should win, got %q", rt.Model)
	}
	if !slices.Equal(rt.Domains, []string{"x.com", "y.com"}) {
		t.Errorf("domains: profile should win, got %v", rt.Domains)
	}
	if rt.Backend != "podman" {
		t.Errorf("backend: default should apply, got %q", rt.Backend)
	}
	if rt.HTTPProxy != "http://p" {
		t.Errorf("proxy not carried: %q", rt.HTTPProxy)
	}
	if !rt.Egress {
		t.Error("egress should default true")
	}

	// Nil profile, no overrides → defaults + base domains.
	rt2 := ResolveRuntime(nil, d, "base.com,two.com", "bm", Overrides{})
	if rt2.Agent != "claude" {
		t.Errorf("agent default = %q", rt2.Agent)
	}
	if !slices.Equal(rt2.Domains, []string{"base.com", "two.com"}) {
		t.Errorf("base domains = %v", rt2.Domains)
	}
	if rt2.Model != "bm" {
		t.Errorf("base model = %q", rt2.Model)
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
	path := filepath.Join(dir, "sandboxer.yaml")
	yaml := `name: feature-x
backend: podman
agent: claude
network:
  allowedDomains: [api.anthropic.com, registry.npmjs.org]
roots: [/abs/monorepo, /abs/shared]
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
	if p.Name != "feature-x" || p.Backend != "podman" || p.Agent != "claude" {
		t.Errorf("scalars wrong: %+v", p)
	}
	if len(p.Network.AllowedDomains) != 2 {
		t.Errorf("domains: %v", p.Network.AllowedDomains)
	}
	if len(p.Roots) != 2 || p.Roots[0] != "/abs/monorepo" {
		t.Errorf("roots: %v", p.Roots)
	}
	if len(p.Deps) != 2 || p.Deps[1] != "src/lib/util.go" {
		t.Errorf("deps: %v", p.Deps)
	}
	// JSON serialization uses camelCase keys the srcs package and container read.
	b, err := p.JSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"roots"`, `"deps"`, `"allowedDomains"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("JSON missing %s:\n%s", want, b)
		}
	}
}

func TestExampleProfilesParse(t *testing.T) {
	// The shipped examples must stay valid under the strict (KnownFields) schema.
	for _, name := range []string{"sandboxer.yaml", "with-deps.yaml", "profiles/web.yaml", "profiles/api.yaml"} {
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
