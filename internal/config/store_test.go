package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProfilesDir(t *testing.T) {
	// Explicit override wins.
	t.Setenv("SANDBOXER_PROFILES", "/tmp/p")
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if got := ProfilesDir(); got != "/tmp/p" {
		t.Errorf("SANDBOXER_PROFILES override = %q, want /tmp/p", got)
	}
	// Else XDG.
	t.Setenv("SANDBOXER_PROFILES", "")
	if got := ProfilesDir(); got != filepath.Join("/xdg", "sandboxer", "profiles") {
		t.Errorf("XDG dir = %q", got)
	}
	// Else ~/.config.
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/u")
	if got := ProfilesDir(); got != filepath.Join("/home/u", ".config", "sandboxer", "profiles") {
		t.Errorf("default dir = %q", got)
	}
}

func TestProfileName(t *testing.T) {
	if got := ProfileName("/x/web.yaml", nil); got != "web" {
		t.Errorf("base-name fallback = %q, want web", got)
	}
	if got := ProfileName("/x/web.yaml", &Profile{Name: "prod"}); got != "prod" {
		t.Errorf("explicit name: should override = %q, want prod", got)
	}
	if got := ProfileName("/x/web.yml", &Profile{}); got != "web" {
		t.Errorf(".yml stem = %q, want web", got)
	}
}

func TestListAndFindProfile(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// web.yaml has no name: → effective name is its stem "web".
	write("web.yaml", "backend: docker\nagent: claude\n")
	// prod.yaml sets name: api → effective name "api" (stem is overridden).
	write("prod.yaml", "name: api\nbackend: podman\nagent: opencode\n")
	// a non-YAML file is ignored, a malformed profile is skipped, and a
	// subdirectory is not descended into.
	write("notes.txt", "ignore me\n")
	write("bad.yaml", "bogusField: 1\n")
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	refs := ListProfilesIn(dir)
	if len(refs) != 2 {
		t.Fatalf("ListProfilesIn = %d profiles, want 2: %+v", len(refs), refs)
	}

	// Match by stem.
	if got, err := FindProfile(dir, "web"); err != nil || got != filepath.Join(dir, "web.yaml") {
		t.Errorf("FindProfile web = (%q,%v)", got, err)
	}
	// Match by explicit name:, not by the file's stem.
	if got, err := FindProfile(dir, "api"); err != nil || got != filepath.Join(dir, "prod.yaml") {
		t.Errorf("FindProfile api = (%q,%v)", got, err)
	}
	// The overridden stem "prod" must not resolve.
	if got, err := FindProfile(dir, "prod"); err != nil || got != "" {
		t.Errorf("FindProfile prod = (%q,%v), want ('',nil)", got, err)
	}
	// Unknown name.
	if got, err := FindProfile(dir, "nope"); err != nil || got != "" {
		t.Errorf("FindProfile nope = (%q,%v), want ('',nil)", got, err)
	}
}

func TestFindProfileAmbiguous(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a.yaml", "b.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("name: dup\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := FindProfile(dir, "dup"); err == nil {
		t.Error("two files claiming the same name should be ambiguous")
	}
}

func TestListAllProfiles(t *testing.T) {
	dir := t.TempDir()
	// Project config: a multi-profile doc with a default, a shared agent in
	// defaults: (so web inherits it), and a db section that overrides the agent.
	projectCfg := writeFile(t, dir, "project.yaml",
		"defaults:\n  agent: claude\ndefault: web\nprofiles:\n  web:\n    backend: docker\n  db:\n    backend: podman\n    agent: codex\n")
	// Global config: a shared profile plus a web that the project shadows.
	globalCfg := writeFile(t, dir, "global.yaml",
		"profiles:\n  shared:\n    backend: docker\n    agent: opencode\n  web:\n    backend: podman\n")
	// Store: a db that the project shadows, and a unique api.
	store := t.TempDir()
	writeFile(t, store, "api.yaml", "name: api\nbackend: docker\nagent: claude\n")
	writeFile(t, store, "db.yaml", "backend: podman\nagent: codex\n")

	entries := ListAllProfiles(projectCfg, globalCfg, store)

	get := func(name string, src ProfileSource) *ProfileEntry {
		for i := range entries {
			if entries[i].Name == name && entries[i].Source == src {
				return &entries[i]
			}
		}
		return nil
	}

	// Project web: the default, agent inherited from defaults:, not shadowed.
	if e := get("web", SourceProject); e == nil || !e.IsDefault || e.Shadowed ||
		e.Backend != "docker" || e.Agent != "claude" || e.Path != projectCfg {
		t.Errorf("project web = %+v", e)
	}
	// Project db: overrides the agent, wins over the store's db.
	if e := get("db", SourceProject); e == nil || e.IsDefault || e.Shadowed ||
		e.Backend != "podman" || e.Agent != "codex" {
		t.Errorf("project db = %+v", e)
	}
	// Global shared: unique, never the default (default is project-only).
	if e := get("shared", SourceGlobal); e == nil || e.IsDefault || e.Shadowed || e.Agent != "opencode" {
		t.Errorf("global shared = %+v", e)
	}
	// Global web: shadowed by the project's web.
	if e := get("web", SourceGlobal); e == nil || !e.Shadowed {
		t.Errorf("global web should be shadowed: %+v", e)
	}
	// Store api: unique, kept.
	if e := get("api", SourceStore); e == nil || e.Shadowed {
		t.Errorf("store api = %+v", e)
	}
	// Store db: shadowed by the project's db.
	if e := get("db", SourceStore); e == nil || !e.Shadowed {
		t.Errorf("store db should be shadowed: %+v", e)
	}
	if len(entries) != 6 {
		t.Errorf("ListAllProfiles = %d entries, want 6: %+v", len(entries), entries)
	}
}

func TestListAllProfilesAbsentSources(t *testing.T) {
	// An empty project/global path and a missing store dir all contribute nothing.
	if got := ListAllProfiles("", "", filepath.Join(t.TempDir(), "nope")); len(got) != 0 {
		t.Errorf("all-absent = %d entries, want 0: %+v", len(got), got)
	}
	// A flat single-profile project file yields exactly one entry, flagged as the
	// default (it is the sole profile), with no global or store.
	dir := t.TempDir()
	flat := writeFile(t, dir, "feat.yaml", "backend: docker\nagent: claude\n")
	got := ListAllProfiles(flat, filepath.Join(dir, "missing.yaml"), filepath.Join(dir, "nostore"))
	if len(got) != 1 || got[0].Name != "feat" || got[0].Source != SourceProject || !got[0].IsDefault {
		t.Errorf("flat-only = %+v", got)
	}
}

func TestFindProfileMissingDir(t *testing.T) {
	if got, err := FindProfile(filepath.Join(t.TempDir(), "nope"), "x"); err != nil || got != "" {
		t.Errorf("missing dir = (%q,%v), want ('',nil)", got, err)
	}
	if got, err := FindProfile("", "x"); err != nil || got != "" {
		t.Errorf("empty dir = (%q,%v), want ('',nil)", got, err)
	}
}
