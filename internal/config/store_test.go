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

func TestFindProfileMissingDir(t *testing.T) {
	if got, err := FindProfile(filepath.Join(t.TempDir(), "nope"), "x"); err != nil || got != "" {
		t.Errorf("missing dir = (%q,%v), want ('',nil)", got, err)
	}
	if got, err := FindProfile("", "x"); err != nil || got != "" {
		t.Errorf("empty dir = (%q,%v), want ('',nil)", got, err)
	}
}
