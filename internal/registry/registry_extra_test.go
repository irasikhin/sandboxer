package registry

import (
	"strings"
	"testing"
)

func TestInteractiveCmd(t *testing.T) {
	claude, _ := Get("claude") // nativeSandbox=true
	ic := claude.InteractiveCmd("opus", []string{"a.com"})
	if !strings.Contains(ic, "--settings ") || !strings.Contains(ic, "--model 'opus'") {
		t.Errorf("claude InteractiveCmd = %q", ic)
	}
	if strings.Contains(ic, "-p ") {
		t.Errorf("interactive command should carry no task: %q", ic)
	}

	codex, _ := Get("codex") // nativeSandbox=false, empty model
	if got := codex.InteractiveCmd("", nil); strings.Contains(got, "--model") || strings.Contains(got, "--settings") {
		t.Errorf("codex InteractiveCmd should be bare: %q", got)
	}
}

func TestSettingsJSONEmptyDomains(t *testing.T) {
	// nil domains must serialize as an empty array, not null.
	s := SettingsJSON(nil)
	if !strings.Contains(s, `"allowedDomains":[]`) {
		t.Errorf("SettingsJSON(nil) = %s, want empty allowedDomains array", s)
	}
}
