package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDocumentFlat(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "feat.yaml", "backend: native\nagent: claude\n")
	d, err := LoadDocument(p)
	if err != nil {
		t.Fatal(err)
	}
	if d.Multi() {
		t.Error("a flat profile must not report as multi")
	}
	prof, err := d.Select("")
	if err != nil {
		t.Fatal(err)
	}
	// Name falls back to the file base name.
	if prof.Name != "feat" || prof.Backend != "native" {
		t.Errorf("flat select = %+v", prof)
	}
}

func TestLoadDocumentMulti(t *testing.T) {
	dir := t.TempDir()
	body := `
defaults:
  agent: claude
  network:
    allowedDomains: [api.anthropic.com]
  env:
    LOG: info
profiles:
  web:
    backend: podman
    deps: [shared/ui]
  api:
    backend: native
    model: opus
    env:
      LOG: debug
default: web
`
	p := writeFile(t, dir, "sandboxer.yaml", body)
	d, err := LoadDocument(p)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Multi() {
		t.Fatal("a profiles: file must report as multi")
	}

	// Empty name -> default (web), inheriting the shared defaults.
	web, err := d.Select("")
	if err != nil {
		t.Fatal(err)
	}
	if web.Name != "web" || web.Backend != "podman" {
		t.Errorf("default select = %+v", web)
	}
	if web.Agent != "claude" || len(web.Network.AllowedDomains) != 1 {
		t.Errorf("web did not inherit defaults: %+v", web)
	}
	if web.Env["LOG"] != "info" {
		t.Errorf("web env should inherit LOG=info, got %v", web.Env)
	}

	// Named selection, with per-profile overrides on top of defaults.
	api, err := d.Select("api")
	if err != nil {
		t.Fatal(err)
	}
	if api.Backend != "native" || api.Model != "opus" {
		t.Errorf("api own fields wrong: %+v", api)
	}
	if api.Agent != "claude" || len(api.Network.AllowedDomains) != 1 {
		t.Errorf("api did not inherit defaults: %+v", api)
	}
	if api.Env["LOG"] != "debug" {
		t.Errorf("api env override LOG=debug failed: %v", api.Env)
	}

	// Unknown section is an error listing the available names.
	if _, err := d.Select("nope"); err == nil {
		t.Error("selecting an unknown profile must error")
	}
	if !FileHasSection(p, "api") || FileHasSection(p, "nope") {
		t.Error("FileHasSection mismatch")
	}
}

func TestSelectSoleAndAmbiguous(t *testing.T) {
	dir := t.TempDir()
	// One section, no default: -> selectable with an empty name.
	sole := writeFile(t, dir, "one.yaml", "profiles:\n  only:\n    backend: native\n")
	d, err := LoadDocument(sole)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := d.Select(""); err != nil || p.Name != "only" {
		t.Errorf("sole select = (%v, %v)", p, err)
	}

	// Two sections, no default: -> empty name is ambiguous.
	two := writeFile(t, dir, "two.yaml", "profiles:\n  a: {backend: native}\n  b: {backend: podman}\n")
	d2, err := LoadDocument(two)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d2.Select(""); err == nil {
		t.Error("ambiguous default selection must error")
	}
}

func TestLoadDocumentMultiStrict(t *testing.T) {
	dir := t.TempDir()
	bad := writeFile(t, dir, "bad.yaml", "profiles:\n  x:\n    bogusField: 1\n")
	if _, err := LoadDocument(bad); err == nil {
		t.Error("unknown field inside a section must be rejected (KnownFields)")
	}
}

// TestSelectYAMLAnchors covers cross-profile inheritance via native YAML
// anchors + merge keys (no special config field): one profile is anchored and
// merged into another with `<<`, on top of the shared defaults.
func TestSelectYAMLAnchors(t *testing.T) {
	dir := t.TempDir()
	body := `
defaults:
  agent: claude
  network: { allowedDomains: [a.com] }
profiles:
  api: &api
    backend: native
    model: opus
    env: { TIER: base }
  api-prod:
    <<: *api
    env: { TIER: prod }     # explicit keys win over the merged anchor
`
	p := writeFile(t, dir, "m.yaml", body)
	d, err := LoadDocument(p)
	if err != nil {
		t.Fatal(err)
	}
	prod, err := d.Select("api-prod")
	if err != nil {
		t.Fatal(err)
	}
	if prod.Agent != "claude" || len(prod.Network.AllowedDomains) != 1 {
		t.Errorf("api-prod should inherit defaults agent/domains: %+v", prod)
	}
	if prod.Backend != "native" || prod.Model != "opus" {
		t.Errorf("api-prod should inherit api's fields via the anchor: %+v", prod)
	}
	// The anchor merge is lower priority than the node's own keys.
	if prod.Env["TIER"] != "prod" {
		t.Errorf("api-prod own env should win over the anchor: %v", prod.Env)
	}
}

func TestExampleMultiProfileParses(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "multi-profile.yaml")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("example %s not present", path)
	}
	d, err := LoadDocument(path)
	if err != nil {
		t.Fatalf("multi-profile example failed to load: %v", err)
	}
	if !d.Multi() {
		t.Fatal("multi-profile example should report as multi")
	}
	for _, name := range []string{"web", "api"} {
		p, err := d.Select(name)
		if err != nil {
			t.Errorf("select %s: %v", name, err)
			continue
		}
		if p.Agent != "claude" {
			t.Errorf("%s should inherit the shared agent, got %+v", name, p)
		}
	}
}

func TestMergeProfile(t *testing.T) {
	tru := true
	base := Profile{
		Agent:   "claude",
		Network: Network{AllowedDomains: []string{"a.com"}},
		Env:     map[string]string{"X": "1", "Y": "2"},
		Egress:  &tru,
	}
	over := Profile{
		Backend: "podman",
		Agent:   "opencode", // overrides base
		Env:     map[string]string{"Y": "9", "Z": "3"},
	}
	got := mergeProfile(base, over)
	if got.Agent != "opencode" || got.Backend != "podman" {
		t.Errorf("override fields wrong: %+v", got)
	}
	if len(got.Network.AllowedDomains) != 1 || got.Network.AllowedDomains[0] != "a.com" {
		t.Errorf("unset field should inherit base domains: %+v", got.Network)
	}
	if got.Egress == nil || !*got.Egress {
		t.Errorf("unset *bool should inherit base: %v", got.Egress)
	}
	// Env merges key-wise: base X kept, Y overridden, Z added.
	if got.Env["X"] != "1" || got.Env["Y"] != "9" || got.Env["Z"] != "3" {
		t.Errorf("env key-merge wrong: %v", got.Env)
	}
}
