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
	path := writeFile(t, dir, "config.yaml", "defaults:\n  agent: codex\nprofiles:\n  shared:\n    backend: docker\n")
	t.Setenv("SANDBOXER_CONFIG", path)
	d, err = LoadGlobalConfig()
	if err != nil {
		t.Fatalf("present global config errored: %v", err)
	}
	if d == nil || d.Defaults.Agent != "codex" || !d.Has("shared") {
		t.Fatalf("present global config not parsed: %+v", d)
	}
}

// TestLoadDocumentDefaultsOnly checks that a file carrying only a defaults:
// block (no profiles:) parses as a Document — the common shape of the global
// config — instead of failing strict parse as a flat Profile.
func TestLoadDocumentDefaultsOnly(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "config.yaml", "defaults:\n  agent: codex\n  backend: podman\n")
	d, err := LoadDocument(p)
	if err != nil {
		t.Fatalf("defaults-only document errored: %v", err)
	}
	if !d.Multi() {
		t.Error("defaults-only document should be in the multi/document form")
	}
	if d.Defaults.Agent != "codex" || d.Defaults.Backend != "podman" {
		t.Errorf("defaults not parsed: %+v", d.Defaults)
	}
	if len(d.Profiles) != 0 {
		t.Errorf("defaults-only document should have no profiles, got %v", d.Profiles)
	}
}

// TestResolveWithGlobalDefaults shows a project profile inheriting a global
// default for a field the project leaves unset.
func TestResolveWithGlobalDefaults(t *testing.T) {
	global := &Document{Defaults: Profile{Agent: "codex", Backend: "podman"}}
	project := &Document{
		Defaults: Profile{}, // project sets no agent/backend
		Profiles: map[string]Profile{"web": {Session: "ephemeral"}},
		Default:  "web",
	}

	got, err := project.SelectWithGlobal("web", "", "", global)
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent != "codex" {
		t.Errorf("Agent = %q, want codex (inherited from global defaults)", got.Agent)
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
	global := &Document{Defaults: Profile{Agent: "codex", Session: "persistent"}}
	project := &Document{
		Defaults: Profile{Agent: "claude"}, // project default beats global default
		Profiles: map[string]Profile{"web": {Session: "ephemeral"}},
		Default:  "web",
	}

	got, err := project.SelectWithGlobal("web", "", "", global)
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent != "claude" {
		t.Errorf("Agent = %q, want claude (project defaults beat global defaults)", got.Agent)
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

	got, err := project.SelectWithGlobal("web", "", "", global)
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

	got, err = project.SelectWithGlobal("web", "", "", global)
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
		Defaults: Profile{Agent: "claude"},
		Profiles: map[string]Profile{"web": {Session: "ephemeral"}},
		Default:  "web",
	}

	withNil, err := project.SelectWithGlobal("web", "", "", nil)
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
		Profiles: map[string]Profile{"ops": {Session: "ephemeral"}},
	}
	got, err := project.SelectWithGlobal("ops", "", "", global)
	if err != nil {
		t.Fatalf("global-only profile should resolve: %v", err)
	}
	if got.Name != "ops" || got.Session != "ephemeral" {
		t.Errorf("global-only profile = %+v, want name=ops session=ephemeral", got)
	}
	if got.Agent != "claude" {
		t.Errorf("Agent = %q, want claude (project default kept under a global profile)", got.Agent)
	}
	if got.Backend != "podman" {
		t.Errorf("Backend = %q, want podman (from global defaults)", got.Backend)
	}

	// An unknown name (neither project nor global) still errors.
	if _, err := project.SelectWithGlobal("nope", "", "", global); err == nil {
		t.Error("unknown profile name should error")
	}
}

// TestAgentProxyResolution pins the per-agent proxy precedence:
// section.proxy > agentProxy[agent] > defaults.proxy, project agentProxy over
// global, keyed by the agent that will run (flag > profile > env default).
func TestAgentProxyResolution(t *testing.T) {
	// section proxy wins over a per-agent entry for the same agent.
	d := &Document{
		Profiles:   map[string]Profile{"web": {Agent: "claude", Network: Network{Proxy: "http://section:1"}}},
		Default:    "web",
		AgentProxy: map[string]string{"claude": "http://agent:2"},
	}
	got, err := d.SelectWithGlobal("web", "", "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Network.Proxy != "http://section:1" {
		t.Errorf("section proxy must win: %q", got.Network.Proxy)
	}

	// no section proxy: agentProxy[agent] beats the defaults proxy.
	d2 := &Document{
		Defaults:   Profile{Network: Network{Proxy: "http://default:3"}},
		Profiles:   map[string]Profile{"web": {Agent: "codex"}},
		Default:    "web",
		AgentProxy: map[string]string{"codex": "http://agent:4"},
	}
	got2, err := d2.SelectWithGlobal("web", "", "claude", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got2.Network.Proxy != "http://agent:4" {
		t.Errorf("agentProxy must beat defaults proxy: %q", got2.Network.Proxy)
	}

	// the --agent flag drives which agentProxy entry is picked.
	got3, err := d2.SelectWithGlobal("web", "claude", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got3.Network.Proxy != "http://default:3" {
		t.Errorf("flag agent=claude has no agentProxy entry, so defaults apply: %q", got3.Network.Proxy)
	}

	// project agentProxy overrides a global one for the same agent; a global-only
	// agent entry still applies.
	global := &Document{AgentProxy: map[string]string{"claude": "http://gclaude", "codex": "http://gcodex"}}
	project := &Document{
		Profiles:   map[string]Profile{"web": {Agent: "claude"}},
		Default:    "web",
		AgentProxy: map[string]string{"claude": "http://pclaude"},
	}
	got4, err := project.SelectWithGlobal("web", "", "claude", global)
	if err != nil {
		t.Fatal(err)
	}
	if got4.Network.Proxy != "http://pclaude" {
		t.Errorf("project agentProxy must beat global: %q", got4.Network.Proxy)
	}
	got5, err := project.SelectWithGlobal("web", "codex", "", global)
	if err != nil {
		t.Fatal(err)
	}
	if got5.Network.Proxy != "http://gcodex" {
		t.Errorf("global-only agentProxy entry should apply: %q", got5.Network.Proxy)
	}
}
