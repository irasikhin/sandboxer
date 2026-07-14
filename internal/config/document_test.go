package config

import (
	"os"
	"path/filepath"
	"strings"
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
	p := writeFile(t, dir, "feat.yaml", "backend: docker\n")
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
	if prof.Name != "feat" || prof.Backend != "docker" {
		t.Errorf("flat select = %+v", prof)
	}
}

func TestLoadDocumentMulti(t *testing.T) {
	dir := t.TempDir()
	body := `
profiles:
  web:
    backend: podman
    deps: [shared/ui]
    env:
      LOG: info
  api:
    backend: docker
    session: ephemeral
    env:
      LOG: debug
default: web
`
	p := writeFile(t, dir, ConfigFileName, body)
	d, err := LoadDocument(p)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Multi() {
		t.Fatal("a profiles: file must report as multi")
	}

	// Empty name -> default (web). Sections are self-contained.
	web, err := d.Select("")
	if err != nil {
		t.Fatal(err)
	}
	if web.Name != "web" || web.Backend != "podman" {
		t.Errorf("default select = %+v", web)
	}
	if web.Env["LOG"] != "info" {
		t.Errorf("web env = %v, want LOG=info", web.Env)
	}

	// Named selection reads exactly that section.
	api, err := d.Select("api")
	if err != nil {
		t.Fatal(err)
	}
	if api.Backend != "docker" || api.Session != "ephemeral" {
		t.Errorf("api own fields wrong: %+v", api)
	}
	if api.Env["LOG"] != "debug" {
		t.Errorf("api env = %v, want LOG=debug", api.Env)
	}

	// Unknown section is an error listing the available names.
	if _, err := d.Select("nope"); err == nil {
		t.Error("selecting an unknown profile must error")
	}
	if !FileHasProfile(p, "api") || FileHasProfile(p, "nope") {
		t.Error("FileHasProfile (multi) mismatch")
	}

	// Flat file: the single profile's effective name matches too.
	flat := writeFile(t, t.TempDir(), "x.yaml", "name: solo\nbackend: docker\n")
	if !FileHasProfile(flat, "solo") || FileHasProfile(flat, "nope") {
		t.Error("FileHasProfile (flat) mismatch")
	}
	if FileHasProfile(filepath.Join(t.TempDir(), "missing.yaml"), "solo") {
		t.Error("FileHasProfile on a missing file should be false")
	}
}

func TestSelectSoleAndAmbiguous(t *testing.T) {
	dir := t.TempDir()
	// One section, no default: -> selectable with an empty name.
	sole := writeFile(t, dir, "one.yaml", "profiles:\n  only:\n    backend: docker\n")
	d, err := LoadDocument(sole)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := d.Select(""); err != nil || p.Name != "only" {
		t.Errorf("sole select = (%v, %v)", p, err)
	}

	// Two sections, no default: -> empty name is ambiguous.
	two := writeFile(t, dir, "two.yaml", "profiles:\n  a: {backend: docker}\n  b: {backend: podman}\n")
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

// TestSelectYAMLAnchors covers cross-profile reuse via native YAML anchors +
// merge keys (no special config field): one profile is anchored and merged
// into another with `<<` — the only sharing mechanism now that there is no
// defaults: layer.
func TestSelectYAMLAnchors(t *testing.T) {
	dir := t.TempDir()
	body := `
profiles:
  api: &api
    backend: docker
    session: ephemeral
    network: { allowedDomains: [a.com] }
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
	if len(prod.Network.AllowedDomains) != 1 {
		t.Errorf("api-prod should inherit api's domains via the anchor: %+v", prod)
	}
	if prod.Backend != "docker" || prod.Session != "ephemeral" {
		t.Errorf("api-prod should inherit api's fields via the anchor: %+v", prod)
	}
	// The anchor merge is lower priority than the node's own keys.
	if prod.Env["TIER"] != "prod" {
		t.Errorf("api-prod own env should win over the anchor: %v", prod.Env)
	}
}

// TestLoadDocumentRejectsDefaults: the removed defaults: layer gets the
// migration hint, in both the flat and the profiles: shape.
func TestLoadDocumentRejectsDefaults(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"only.yaml": "defaults:\n  backend: docker\n",
		"both.yaml": "defaults:\n  backend: docker\nprofiles:\n  web:\n    backend: podman\n",
	} {
		p := writeFile(t, dir, name, body)
		_, err := LoadDocument(p)
		if err == nil || !strings.Contains(err.Error(), "self-contained") {
			t.Errorf("%s: err = %v, want the defaults: migration hint", name, err)
		}
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
		if len(p.Network.AllowedDomains) == 0 {
			t.Errorf("%s should inherit the shared allowlist, got %+v", name, p)
		}
	}
}
