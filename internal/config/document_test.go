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
	p := writeFile(t, dir, "feat.nix", "{ backend = \"docker\"; }\n")
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
	body := `{
  profiles = {
    web = {
      backend = "podman";
      srcs = [ { src = "."; include = [ "/shared/ui/" ]; } ];
      env.LOG = "info";
    };
    api = {
      backend = "docker";
      session = "ephemeral";
      env.LOG = "debug";
    };
  };
  default = "web";
}
`
	p := writeFile(t, dir, ConfigFileName, body)
	d, err := LoadDocument(p)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Multi() {
		t.Fatal("a profiles config must report as multi")
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
	flat := writeFile(t, t.TempDir(), "x.nix", "{ name = \"solo\"; backend = \"docker\"; }\n")
	if !FileHasProfile(flat, "solo") || FileHasProfile(flat, "nope") {
		t.Error("FileHasProfile (flat) mismatch")
	}
	if FileHasProfile(filepath.Join(t.TempDir(), "missing.nix"), "solo") {
		t.Error("FileHasProfile on a missing file should be false")
	}
}

func TestSelectSoleAndAmbiguous(t *testing.T) {
	dir := t.TempDir()
	// One section, no default -> selectable with an empty name.
	sole := writeFile(t, dir, "one.nix", "{ profiles.only.backend = \"docker\"; }\n")
	d, err := LoadDocument(sole)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := d.Select(""); err != nil || p.Name != "only" {
		t.Errorf("sole select = (%v, %v)", p, err)
	}

	// Two sections, no default -> empty name is ambiguous.
	two := writeFile(t, dir, "two.nix",
		"{ profiles.a.backend = \"docker\"; profiles.b.backend = \"podman\"; }\n")
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
	bad := writeFile(t, dir, "bad.nix", "{ profiles.x.bogusField = 1; }\n")
	if _, err := LoadDocument(bad); err == nil {
		t.Error("unknown field inside a section must be rejected (strict decode)")
	}
	// Unknown top-level keys in the multi form are rejected too.
	top := writeFile(t, dir, "top.nix", "{ profiles.x.backend = \"docker\"; extra = 1; }\n")
	if _, err := LoadDocument(top); err == nil || !strings.Contains(err.Error(), "unknown top-level key") {
		t.Errorf("unknown top-level key = %v, want rejection", err)
	}
	// A config that does not evaluate to an attrset is rejected with the
	// contract named.
	lst := writeFile(t, dir, "list.nix", "[ 1 2 ]\n")
	if _, err := LoadDocument(lst); err == nil || !strings.Contains(err.Error(), "attrset") {
		t.Errorf("non-attrset config = %v, want the attrset contract error", err)
	}
	// A broken expression surfaces nix's own error.
	syn := writeFile(t, dir, "syn.nix", "{ oops\n")
	if _, err := LoadDocument(syn); err == nil || !strings.Contains(err.Error(), "nix eval failed") {
		t.Errorf("syntax error = %v, want a nix eval failure", err)
	}
}

// TestSelectNixReuse covers cross-profile reuse with ordinary nix — a
// let-bound base merged into sections with // — the only sharing mechanism
// now that there is no defaults layer.
func TestSelectNixReuse(t *testing.T) {
	dir := t.TempDir()
	body := `let
  api = {
    backend = "docker";
    session = "ephemeral";
    egress.allowedDomains = [ "a.com" ];
    env.TIER = "base";
  };
in {
  profiles = {
    inherit api;
    api-prod = api // { env.TIER = "prod"; };  # rightmost // operand wins
  };
}
`
	p := writeFile(t, dir, "m.nix", body)
	d, err := LoadDocument(p)
	if err != nil {
		t.Fatal(err)
	}
	prod, err := d.Select("api-prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(prod.Egress.AllowedDomains) != 1 {
		t.Errorf("api-prod should inherit api's domains via the base: %+v", prod)
	}
	if prod.Backend != "docker" || prod.Session != "ephemeral" {
		t.Errorf("api-prod should inherit api's fields via the base: %+v", prod)
	}
	// The merge's right operand wins.
	if prod.Env["TIER"] != "prod" {
		t.Errorf("api-prod own env should win over the base: %v", prod.Env)
	}
}

// TestLoadDocumentRejectsDefaults: the removed defaults layer gets the
// migration hint, in both the flat and the profiles shape.
func TestLoadDocumentRejectsDefaults(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"only.nix": "{ defaults.backend = \"docker\"; }\n",
		"both.nix": "{ defaults.backend = \"docker\"; profiles.web.backend = \"podman\"; }\n",
	} {
		p := writeFile(t, dir, name, body)
		_, err := LoadDocument(p)
		if err == nil || !strings.Contains(err.Error(), "self-contained") {
			t.Errorf("%s: err = %v, want the defaults migration hint", name, err)
		}
	}
}

func TestExampleMultiProfileParses(t *testing.T) {
	path := filepath.Join("..", "..", "examples", "multi-profile.nix")
	d, err := LoadDocument(path)
	if err != nil {
		t.Fatalf("multi-profile example failed to load: %v", err)
	}
	if !d.Multi() {
		t.Fatal("multi-profile example should report as multi")
	}
	for _, name := range []string{"web", "api", "api-prod"} {
		p, err := d.Select(name)
		if err != nil {
			t.Errorf("select %s: %v", name, err)
			continue
		}
		if len(p.Egress.AllowedDomains) == 0 {
			t.Errorf("%s should inherit the shared allowlist, got %+v", name, p)
		}
	}
}
