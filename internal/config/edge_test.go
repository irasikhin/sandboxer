package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDocumentErrors(t *testing.T) {
	dir := t.TempDir()

	// missing file → ReadFile error
	if _, err := LoadDocument(filepath.Join(dir, "nope.yaml")); err == nil {
		t.Error("LoadDocument(missing) = nil error, want error")
	}

	// malformed YAML → fails the multi-profile probe
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("foo: [unterminated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDocument(bad); err == nil {
		t.Error("LoadDocument(malformed) = nil error, want error")
	}

	// valid YAML but an unknown field → strict flat-profile decode fails
	unknown := filepath.Join(dir, "unknown.yaml")
	if err := os.WriteFile(unknown, []byte("bogusfield: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDocument(unknown); err == nil {
		t.Error("LoadDocument(unknown field) = nil error, want error")
	}
}

func TestResolveRuntimeValidation(t *testing.T) {
	// a domain with whitespace is rejected (call site + ValidateDomains)
	if _, err := ResolveRuntime(&Profile{}, Defaults{}, "", "", Overrides{Domains: "bad domain"}); err == nil {
		t.Error("ResolveRuntime(invalid domain) = nil error, want error")
	}

	// an https proxy is rejected while the egress allowlist is on (chained mode
	// cannot speak TLS to a parent)
	p := &Profile{Proxy: "https://corp:8080"}
	if _, err := ResolveRuntime(p, Defaults{}, "", "", Overrides{}); err == nil {
		t.Error("ResolveRuntime(https proxy + egress on) = nil error, want error")
	}
}

func TestProfilesDirNoHome(t *testing.T) {
	t.Setenv("SANDBOXER_PROFILES", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "") // os.UserHomeDir errors → "" returned

	if got := ProfilesDir(); got != "" {
		t.Errorf("ProfilesDir() with no HOME = %q, want \"\"", got)
	}
}
