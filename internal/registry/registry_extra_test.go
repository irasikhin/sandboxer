package registry

import (
	"strings"
	"testing"
)

func TestInteractiveCmd(t *testing.T) {
	claude, _ := Get("claude")
	ic := claude.InteractiveCmd("opus", []string{"a.com"})
	if !strings.Contains(ic, "--model 'opus'") {
		t.Errorf("claude InteractiveCmd = %q", ic)
	}
	// The native /sandbox --settings flag was removed with the native backend.
	if strings.Contains(ic, "--settings") {
		t.Errorf("interactive command must not carry --settings: %q", ic)
	}
	if strings.Contains(ic, "-p ") {
		t.Errorf("interactive command should carry no task: %q", ic)
	}

	codex, _ := Get("codex") // empty model
	if got := codex.InteractiveCmd("", nil); strings.Contains(got, "--model") || strings.Contains(got, "--settings") {
		t.Errorf("codex InteractiveCmd should be bare: %q", got)
	}
}
