package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// flatFixture mirrors the scaffolded flat config: heavily commented, an
// inline comment, an existing nested section.
const flatFixture = `# yaml-language-server: $schema=https://example.com/schema.json
# sandboxer profile — edit to taste.

# Sandbox name (slug); drives the worktree branch sandbox/<name>.
name: feat

# Isolation backend: docker | podman.
backend: docker

# Egress allowlist — trim to what your task needs.
network:
  allowedDomains: [api.anthropic.com, github.com] # inline note
`

// multiFixture mirrors examples/multi-profile.yaml: anchors, a merge key, an
// alias-valued section and a null (empty) section.
const multiFixture = `# per-project profiles — each section is self-contained
profiles:
  # the web profile
  web:
    backend: podman # web runs podman
  api: &api
    backend: docker
    deps: [src/api]
  api-prod:
    <<: *api
    session: ephemeral
  mirror: *api
  empty:

default: web
`

// editFixture parses data, applies edit, and returns the re-encoded bytes.
func editFixture(t *testing.T, data string, edit func(*EditableConfig)) string {
	t.Helper()
	ed, err := ParseEditable([]byte(data))
	if err != nil {
		t.Fatalf("ParseEditable: %v", err)
	}
	edit(ed)
	out, err := ed.Bytes()
	if err != nil {
		t.Fatalf("Bytes: %v", err)
	}
	return string(out)
}

func mustSet(t *testing.T, ed *EditableConfig, path []string, raw string) {
	t.Helper()
	var v yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("value %q: %v", raw, err)
	}
	if err := ed.Set(path, v.Content[0]); err != nil {
		t.Fatalf("Set(%v): %v", path, err)
	}
}

// TestEditableSetFlat: replacing a scalar keeps every comment in the file and
// the result still strict-decodes with the new value.
func TestEditableSetFlat(t *testing.T) {
	out := editFixture(t, flatFixture, func(ed *EditableConfig) {
		if ed.Multi() {
			t.Error("flat fixture reported multi")
		}
		mustSet(t, ed, []string{"backend"}, "podman")
	})
	for _, comment := range []string{
		"# yaml-language-server:",
		"# sandboxer profile — edit to taste.",
		"# Sandbox name (slug); drives the worktree branch sandbox/<name>.",
		"# Isolation backend: docker | podman.",
		"# Egress allowlist — trim to what your task needs.",
		"# inline note",
	} {
		if !strings.Contains(out, comment) {
			t.Errorf("comment lost after set:\n%s\nmissing %q", out, comment)
		}
	}
	d, err := LoadDocumentBytes([]byte(out), "config.yaml")
	if err != nil {
		t.Fatalf("edited file no longer decodes: %v\n%s", err, out)
	}
	if p, _ := d.Select(""); p.Backend != "podman" {
		t.Errorf("backend = %q, want podman", p.Backend)
	}
}

// TestEditableSetCreatesIntermediates: a dotted path with no existing section
// creates the intermediate mappings.
func TestEditableSetCreatesIntermediates(t *testing.T) {
	out := editFixture(t, flatFixture, func(ed *EditableConfig) {
		mustSet(t, ed, []string{"limits", "memory"}, "4G")
	})
	d, err := LoadDocumentBytes([]byte(out), "config.yaml")
	if err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if p, _ := d.Select(""); p.Limits.Memory != "4G" {
		t.Errorf("limits.memory = %q, want 4G", p.Limits.Memory)
	}
	if !strings.Contains(out, "limits:\n  memory: 4G") {
		t.Errorf("expected 2-space indented limits block:\n%s", out)
	}
}

// TestEditableSetProfileSection: a profiles.<name>.… path edits only that
// section; sibling sections are untouched.
func TestEditableSetProfileSection(t *testing.T) {
	out := editFixture(t, multiFixture, func(ed *EditableConfig) {
		if !ed.Multi() {
			t.Error("multi fixture reported flat")
		}
		mustSet(t, ed, []string{"profiles", "web", "network", "proxy"}, "http://localhost:3128")
	})
	d, err := LoadDocumentBytes([]byte(out), "config.yaml")
	if err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	web, _ := d.Select("web")
	if web.Network.Proxy != "http://localhost:3128" {
		t.Errorf("web proxy = %q", web.Network.Proxy)
	}
	if api, _ := d.Select("api"); api.Network.Proxy != "" {
		t.Errorf("api gained a proxy: %q", api.Network.Proxy)
	}
	for _, comment := range []string{"# per-project profiles — each section is self-contained", "# the web profile", "# web runs podman"} {
		if !strings.Contains(out, comment) {
			t.Errorf("comment lost:\n%s\nmissing %q", out, comment)
		}
	}
}

// TestEditableAnchorsSurvive: editing inside an anchored section keeps the
// anchor and the merge key working — the inheriting profile re-resolves the
// NEW value on the next parse.
func TestEditableAnchorsSurvive(t *testing.T) {
	out := editFixture(t, multiFixture, func(ed *EditableConfig) {
		mustSet(t, ed, []string{"profiles", "api", "deps"}, "[src/api, src/shared]")
	})
	if !strings.Contains(out, "&api") || !strings.Contains(out, "!!merge <<: *api") && !strings.Contains(out, "<<: *api") {
		t.Fatalf("anchor or merge key lost:\n%s", out)
	}
	d, err := LoadDocumentBytes([]byte(out), "config.yaml")
	if err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	prod, err := d.Select("api-prod")
	if err != nil {
		t.Fatalf("select api-prod: %v", err)
	}
	if len(prod.Deps) != 2 || prod.Deps[1] != "src/shared" {
		t.Errorf("api-prod did not inherit the edited deps: %v", prod.Deps)
	}
}

// TestEditableAliasRefused: descending through an alias-valued section is an
// error, not a silent edit of the shared anchored node.
func TestEditableAliasRefused(t *testing.T) {
	ed, err := ParseEditable([]byte(multiFixture))
	if err != nil {
		t.Fatal(err)
	}
	err = ed.Set([]string{"profiles", "mirror", "backend"}, keyNode("podman"))
	if err == nil || !strings.Contains(err.Error(), "alias") || !strings.Contains(err.Error(), "*api") {
		t.Errorf("alias set error = %v", err)
	}
	if _, err := ed.Unset([]string{"profiles", "mirror", "backend"}); err == nil || !strings.Contains(err.Error(), "alias") {
		t.Errorf("alias unset error = %v", err)
	}
}

// TestEditableReplaceKeepsCommentAndAnchor: replacing a value node carries its
// inline comment and anchor onto the new node.
func TestEditableReplaceKeepsCommentAndAnchor(t *testing.T) {
	const in = "deps: &d [src] # keep me\nother: *d\n"
	out := editFixture(t, in, func(ed *EditableConfig) {
		mustSet(t, ed, []string{"deps"}, "[src, docs]")
	})
	if !strings.Contains(out, "&d") {
		t.Errorf("anchor dropped — aliases elsewhere would break:\n%s", out)
	}
	if !strings.Contains(out, "# keep me") {
		t.Errorf("inline comment dropped:\n%s", out)
	}
	var round map[string][]string
	if err := yaml.Unmarshal([]byte(out), &round); err != nil {
		t.Fatalf("re-parse: %v\n%s", err, out)
	}
	if len(round["other"]) != 2 {
		t.Errorf("alias no longer resolves to the new value: %v", round["other"])
	}
}

// TestEditableNullSectionUpgrade: an empty section (`empty:`) is upgraded to a
// mapping in place on set.
func TestEditableNullSectionUpgrade(t *testing.T) {
	out := editFixture(t, multiFixture, func(ed *EditableConfig) {
		mustSet(t, ed, []string{"profiles", "empty", "backend"}, "docker")
	})
	d, err := LoadDocumentBytes([]byte(out), "config.yaml")
	if err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if p, _ := d.Select("empty"); p.Backend != "docker" {
		t.Errorf("empty section backend = %q", p.Backend)
	}
}

// TestEditableErrors: non-mapping intermediates, empty paths and multi-doc
// streams are rejected; an empty file is a valid empty mapping.
func TestEditableErrors(t *testing.T) {
	ed, err := ParseEditable([]byte(flatFixture))
	if err != nil {
		t.Fatal(err)
	}
	if err := ed.Set([]string{"backend", "sub"}, keyNode("x")); err == nil || !strings.Contains(err.Error(), "backend is not a mapping") {
		t.Errorf("scalar intermediate error = %v", err)
	}
	if err := ed.Set(nil, keyNode("x")); err == nil {
		t.Error("empty path Set should error")
	}
	if _, err := ed.Unset(nil); err == nil {
		t.Error("empty path Unset should error")
	}

	if _, err := ParseEditable([]byte("a: 1\n---\nb: 2\n")); err == nil || !strings.Contains(err.Error(), "multiple yaml documents") {
		t.Errorf("multi-doc error = %v", err)
	}
	if _, err := ParseEditable([]byte("- a\n- b\n")); err == nil || !strings.Contains(err.Error(), "not a yaml mapping") {
		t.Errorf("sequence-top error = %v", err)
	}
	if _, err := ParseEditable([]byte(":bad")); err == nil {
		t.Error("garbage input should error")
	}

	out := editFixture(t, "", func(e *EditableConfig) {
		mustSet(t, e, []string{"backend"}, "podman")
	})
	if !strings.Contains(out, "backend: podman") {
		t.Errorf("empty-file set:\n%s", out)
	}
	outNull := editFixture(t, "---\n", func(e *EditableConfig) {
		mustSet(t, e, []string{"backend"}, "podman")
	})
	if !strings.Contains(outNull, "backend: podman") {
		t.Errorf("null-doc set:\n%s", outNull)
	}
}

// TestEditableUnset: present keys are removed; absent and merge-inherited
// keys report (false, nil); an emptied parent still decodes.
func TestEditableUnset(t *testing.T) {
	ed, err := ParseEditable([]byte(multiFixture))
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := ed.Unset([]string{"profiles", "web", "backend"}); !ok || err != nil {
		t.Fatalf("unset existing = (%v, %v)", ok, err)
	}
	if ok, err := ed.Unset([]string{"profiles", "web", "backend"}); ok || err != nil {
		t.Errorf("unset absent = (%v, %v), want (false, nil)", ok, err)
	}
	// api-prod only inherits deps via <<: *api — its own section has no deps.
	if ok, err := ed.Unset([]string{"profiles", "api-prod", "deps"}); ok || err != nil {
		t.Errorf("unset merge-inherited = (%v, %v), want (false, nil)", ok, err)
	}
	// A missing intermediate section is absent, not an error.
	if ok, err := ed.Unset([]string{"profiles", "nosuch", "backend"}); ok || err != nil {
		t.Errorf("unset missing section = (%v, %v), want (false, nil)", ok, err)
	}
	out, err := ed.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	d, err := LoadDocumentBytes(out, "config.yaml")
	if err != nil {
		t.Fatalf("emptied section no longer decodes: %v\n%s", err, out)
	}
	if p, _ := d.Select("web"); p.Backend != "" {
		t.Errorf("web backend after unset = %q, want empty (sections are self-contained)", p.Backend)
	}
}

// TestEditableEnvKeys: env vars address as env.<NAME> map entries.
func TestEditableEnvKeys(t *testing.T) {
	ed, err := ParseEditable([]byte("env:\n  FOO: a\n"))
	if err != nil {
		t.Fatal(err)
	}
	mustSet(t, ed, []string{"env", "FOO"}, "b")
	mustSet(t, ed, []string{"env", "BAR"}, "c")
	if ok, err := ed.Unset([]string{"env", "FOO"}); !ok || err != nil {
		t.Fatalf("unset env.FOO = (%v, %v)", ok, err)
	}
	out, err := ed.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	var round struct {
		Env map[string]string `yaml:"env"`
	}
	if err := yaml.Unmarshal(out, &round); err != nil {
		t.Fatal(err)
	}
	if len(round.Env) != 1 || round.Env["BAR"] != "c" {
		t.Errorf("env after edits = %v", round.Env)
	}
}

// TestEditableStyleAndTail: a flow list stays flow, output is 2-space
// indented and ends with a newline.
func TestEditableStyleAndTail(t *testing.T) {
	out := editFixture(t, flatFixture, func(ed *EditableConfig) {
		mustSet(t, ed, []string{"network", "allowedDomains"}, "[a.com, b.com]")
	})
	if !strings.Contains(out, "allowedDomains: [a.com, b.com]") {
		t.Errorf("flow list not kept flow:\n%s", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Error("output must end with a newline")
	}
	if !strings.Contains(out, "\n  allowedDomains:") {
		t.Errorf("nested key not 2-space indented:\n%s", out)
	}
}
