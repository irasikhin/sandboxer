package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
)

// TestBackendLabelMicrovm pins the banner label for the microVM backend.
func TestBackendLabelMicrovm(t *testing.T) {
	if got := backendLabel(config.Runtime{Backend: "microvm"}); got != "microvm (smolvm)" {
		t.Errorf("backendLabel(microvm) = %q, want %q", got, "microvm (smolvm)")
	}
}

// TestWarnMicrovmProxy pins the advisory when a microvm sandbox has both a proxy
// and an allowlist (the allowlist is proxy-enforced), and its silence otherwise.
func TestWarnMicrovmProxy(t *testing.T) {
	var b bytes.Buffer
	warnMicrovmProxy(&b, config.Runtime{Backend: "microvm", Proxy: "http://p:3128", Domains: []string{"a.com"}})
	if !strings.Contains(b.String(), "enforced by the proxy") {
		t.Errorf("missing proxy/allowlist advisory: %q", b.String())
	}
	// No allowlist → no advisory.
	for _, rt := range []config.Runtime{
		{Backend: "microvm", Proxy: "http://p:3128"},
	} {
		b.Reset()
		warnMicrovmProxy(&b, rt)
		if b.Len() != 0 {
			t.Errorf("unexpected advisory for %+v: %q", rt, b.String())
		}
	}
}
