package config

import (
	"path/filepath"
	"reflect"
	"testing"
)

// TestGlobalConfigPath pins the resolution order: SANDBOXER_CONFIG override,
// then $XDG_CONFIG_HOME, then ~/.config — and "" when no home can be found.
func TestGlobalConfigPath(t *testing.T) {
	// Explicit override wins.
	t.Setenv("SANDBOXER_CONFIG", "/tmp/g.yaml")
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	t.Setenv("HOME", "/home/u")
	if got := GlobalConfigPath(); got != "/tmp/g.yaml" {
		t.Errorf("SANDBOXER_CONFIG override = %q, want /tmp/g.yaml", got)
	}
	// Else XDG.
	t.Setenv("SANDBOXER_CONFIG", "")
	if got := GlobalConfigPath(); got != filepath.Join("/xdg", "sandboxer", "config.yaml") {
		t.Errorf("XDG path = %q", got)
	}
	// Else ~/.config.
	t.Setenv("XDG_CONFIG_HOME", "")
	if got := GlobalConfigPath(); got != filepath.Join("/home/u", ".config", "sandboxer", "config.yaml") {
		t.Errorf("default path = %q", got)
	}
	// No override and no home → "".
	t.Setenv("HOME", "")
	if got := GlobalConfigPath(); got != "" {
		t.Errorf("no home should yield \"\", got %q", got)
	}
}

// TestLoadGlobalConfig reads an existing global file and returns (nil, nil)
// when it is absent — the clean no-op every caller relies on.
func TestLoadGlobalConfig(t *testing.T) {
	dir := t.TempDir()

	// Absent file: clean no-op.
	t.Setenv("SANDBOXER_CONFIG", filepath.Join(dir, "missing.yaml"))
	d, err := LoadGlobalConfig()
	if err != nil {
		t.Fatalf("absent global config errored: %v", err)
	}
	if d != nil {
		t.Fatalf("absent global config = %+v, want nil", d)
	}

	// No path resolvable (no override, no XDG, no home): also a no-op.
	t.Setenv("SANDBOXER_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	if d, err := LoadGlobalConfig(); err != nil || d != nil {
		t.Fatalf("no-home global config = (%+v, %v), want (nil, nil)", d, err)
	}

	// Present file: parsed as a Document.
	path := writeFile(t, dir, "config.yaml", "defaults:\n  backend: podman\nprofiles:\n  shared:\n    backend: docker\n")
	t.Setenv("SANDBOXER_CONFIG", path)
	d, err = LoadGlobalConfig()
	if err != nil {
		t.Fatalf("present global config errored: %v", err)
	}
	if d == nil || d.Defaults.Backend != "podman" || !d.Has("shared") {
		t.Fatalf("present global config not parsed: %+v", d)
	}
}

// TestLoadDocumentDefaultsOnly checks that a file carrying only a defaults:
// block (no profiles:) parses as a Document — the common shape of the global
// config — instead of failing strict parse as a flat Profile.
func TestLoadDocumentDefaultsOnly(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "config.yaml", "defaults:\n  session: ephemeral\n  backend: podman\n")
	d, err := LoadDocument(p)
	if err != nil {
		t.Fatalf("defaults-only document errored: %v", err)
	}
	if !d.Multi() {
		t.Error("defaults-only document should be in the multi/document form")
	}
	if d.Defaults.Session != "ephemeral" || d.Defaults.Backend != "podman" {
		t.Errorf("defaults not parsed: %+v", d.Defaults)
	}
	if len(d.Profiles) != 0 {
		t.Errorf("defaults-only document should have no profiles, got %v", d.Profiles)
	}
}

// TestResolveWithGlobalDefaults shows a project profile inheriting a global
// default for a field the project leaves unset.
func TestResolveWithGlobalDefaults(t *testing.T) {
	global := &Document{Defaults: Profile{Backend: "podman"}}
	project := &Document{
		Defaults: Profile{}, // project sets no backend
		Profiles: map[string]Profile{"web": {Session: "ephemeral"}},
		Default:  "web",
	}

	got, err := project.SelectWithGlobal("web", global)
	if err != nil {
		t.Fatal(err)
	}
	if got.Backend != "podman" {
		t.Errorf("Backend = %q, want podman (inherited from global defaults)", got.Backend)
	}
	if got.Session != "ephemeral" {
		t.Errorf("Session = %q, want ephemeral (from the project profile)", got.Session)
	}
}

// TestProjectOverridesGlobal proves the project always wins: a field set in both
// the global defaults and the project (defaults or section) resolves to the
// project value.
func TestProjectOverridesGlobal(t *testing.T) {
	global := &Document{Defaults: Profile{Backend: "podman", Session: "persistent"}}
	project := &Document{
		Defaults: Profile{Backend: "docker"}, // project default beats global default
		Profiles: map[string]Profile{"web": {Session: "ephemeral"}},
		Default:  "web",
	}

	got, err := project.SelectWithGlobal("web", global)
	if err != nil {
		t.Fatal(err)
	}
	if got.Backend != "docker" {
		t.Errorf("Backend = %q, want docker (project defaults beat global defaults)", got.Backend)
	}
	if got.Session != "ephemeral" {
		t.Errorf("Session = %q, want ephemeral (project section beats global defaults)", got.Session)
	}
}

// TestImageGlobalAndProject checks the per-field image: merge across the layers:
// the global pins the flake revisions while the project adds packages — both
// survive into the effective profile via the existing mergeProfile.
func TestImageGlobalAndProject(t *testing.T) {
	const llmRev = "0123456789abcdef0123456789abcdef01234567"
	const nixRev = "fedcba9876543210fedcba9876543210fedcba98"

	global := &Document{Defaults: Profile{
		Image: ImageSpec{LLMAgentsRev: llmRev, NixpkgsRev: nixRev},
	}}
	project := &Document{
		Profiles: map[string]Profile{"web": {
			Image: ImageSpec{ExtraPkgs: []string{"jq", "ripgrep"}},
		}},
		Default: "web",
	}

	got, err := project.SelectWithGlobal("web", global)
	if err != nil {
		t.Fatal(err)
	}
	if got.Image.LLMAgentsRev != llmRev || got.Image.NixpkgsRev != nixRev {
		t.Errorf("image revs lost: %+v, want global pins", got.Image)
	}
	if len(got.Image.ExtraPkgs) != 2 || got.Image.ExtraPkgs[0] != "jq" {
		t.Errorf("ExtraPkgs = %v, want [jq ripgrep] (from the project)", got.Image.ExtraPkgs)
	}

	// env merges key-wise across the layers too.
	global.Defaults.Env = map[string]string{"GLOBAL_KEY": "g", "SHARED": "global"}
	web := project.Profiles["web"]
	web.Env = map[string]string{"PROJECT_KEY": "p", "SHARED": "project"}
	project.Profiles["web"] = web

	got, err = project.SelectWithGlobal("web", global)
	if err != nil {
		t.Fatal(err)
	}
	if got.Env["GLOBAL_KEY"] != "g" || got.Env["PROJECT_KEY"] != "p" {
		t.Errorf("env not merged key-wise: %v", got.Env)
	}
	if got.Env["SHARED"] != "project" {
		t.Errorf("SHARED = %q, want project (project value wins)", got.Env["SHARED"])
	}
}

// TestGlobalConfigNotRequired confirms a nil global keeps SelectWithGlobal exactly
// equivalent to Select — no global file means unchanged behaviour — and that a
// project name absent from the project but present in the global falls back to
// the global section.
func TestGlobalConfigNotRequired(t *testing.T) {
	project := &Document{
		Defaults: Profile{Session: "persistent"},
		Profiles: map[string]Profile{"web": {Backend: "podman"}},
		Default:  "web",
	}

	withNil, err := project.SelectWithGlobal("web", nil)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := project.Select("web")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(withNil, plain) {
		t.Errorf("nil global diverged from Select:\n nil  %+v\n plain %+v", withNil, plain)
	}

	// Named-profile fallback: a name only in the global resolves to the global
	// section, still over the composed defaults (project default kept).
	global := &Document{
		Defaults: Profile{Backend: "podman"},
		Profiles: map[string]Profile{"ops": {Env: map[string]string{"MODE": "ops"}}},
	}
	got, err := project.SelectWithGlobal("ops", global)
	if err != nil {
		t.Fatalf("global-only profile should resolve: %v", err)
	}
	if got.Name != "ops" || got.Env["MODE"] != "ops" {
		t.Errorf("global-only profile = %+v, want name=ops env MODE=ops", got)
	}
	if got.Session != "persistent" {
		t.Errorf("Session = %q, want persistent (project default kept under a global profile)", got.Session)
	}
	if got.Backend != "podman" {
		t.Errorf("Backend = %q, want podman (from global defaults)", got.Backend)
	}

	// An unknown name (neither project nor global) still errors.
	if _, err := project.SelectWithGlobal("nope", global); err == nil {
		t.Error("unknown profile name should error")
	}
}
