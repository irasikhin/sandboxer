package registry

import (
	"slices"
	"testing"
)

func TestNamesSortedAndComplete(t *testing.T) {
	got := Names()
	want := []string{"aider", "claude", "codex", "crush", "gemini", "opencode", "pi"}
	if !slices.Equal(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
}

func TestGetUnknown(t *testing.T) {
	if _, err := Get("nope"); err == nil {
		t.Fatal("expected error for unknown agent")
	}
}

// TestAgentFields pins the surviving catalog surface: every agent has a binary,
// its auth env vars, and a nix package (the fields the Nix flake and auth
// passthrough actually read).
func TestAgentFields(t *testing.T) {
	for _, name := range Names() {
		a, err := Get(name)
		if err != nil {
			t.Fatal(err)
		}
		if a.Bin == "" {
			t.Errorf("%s: empty bin", name)
		}
		if a.NixPackage == "" {
			t.Errorf("%s: empty nixPackage", name)
		}
		if len(a.AuthEnv) == 0 {
			t.Errorf("%s: no authEnv", name)
		}
	}
}

func TestCodexExcludedFromImage(t *testing.T) {
	a, err := Get("codex")
	if err != nil {
		t.Fatal(err)
	}
	if a.Image == nil || *a.Image {
		t.Error("codex should declare image:false")
	}
	claude, _ := Get("claude")
	if claude.Image != nil {
		t.Error("claude should default to image (nil pointer)")
	}
}
