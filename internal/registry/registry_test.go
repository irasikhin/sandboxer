package registry

import (
	"slices"
	"strings"
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

func TestClaudeHeadlessRendersModel(t *testing.T) {
	a, err := Get("claude")
	if err != nil {
		t.Fatal(err)
	}
	cmd := a.HeadlessCmd("sonnet", []string{"api.anthropic.com"}, "do the thing")
	for _, want := range []string{
		"claude -p 'do the thing'",
		"--permission-mode bypassPermissions",
		"--model 'sonnet'",
		"--output-format json",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("headless cmd missing %q\ngot: %s", want, cmd)
		}
	}
	// The native /sandbox --settings flag was removed with the native backend;
	// egress is enforced by the container proxy, not a per-command settings blob.
	if strings.Contains(cmd, "--settings") {
		t.Errorf("headless cmd must not carry --settings; got: %s", cmd)
	}
}

func TestNonNativeAgentHasNoSettingsFlag(t *testing.T) {
	a, err := Get("opencode")
	if err != nil {
		t.Fatal(err)
	}
	cmd := a.HeadlessCmd("", nil, "task")
	if strings.Contains(cmd, "--settings") {
		t.Errorf("opencode should not get --settings; got: %s", cmd)
	}
	if !strings.Contains(cmd, "opencode run 'task'") {
		t.Errorf("unexpected render: %s", cmd)
	}
	// No model -> no --model flag.
	if strings.Contains(cmd, "--model") {
		t.Errorf("empty model should omit --model; got: %s", cmd)
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

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"":            "''",
		"plain":       "'plain'",
		"with space":  "'with space'",
		"it's a test": `'it'\''s a test'`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}
