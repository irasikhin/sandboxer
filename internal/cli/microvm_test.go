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

// TestWarnMicrovmIgnored pins the advisories for the container-only knobs a
// microVM sandbox drops — and that a container backend emits none of them.
func TestWarnMicrovmIgnored(t *testing.T) {
	var b bytes.Buffer
	prof := &config.Profile{NestedContainers: true}
	warnMicrovmIgnored(&b, config.Runtime{Backend: "microvm", Pids: 512}, prof)
	out := b.String()
	if !strings.Contains(out, "limits.pids ignored") {
		t.Errorf("missing pids warning: %q", out)
	}
	if !strings.Contains(out, "nestedContainers ignored") {
		t.Errorf("missing nestedContainers warning: %q", out)
	}

	// A container backend gets no microvm advisories.
	b.Reset()
	warnMicrovmIgnored(&b, config.Runtime{Backend: "docker", Pids: 512}, prof)
	if b.Len() != 0 {
		t.Errorf("container backend should emit no microvm warnings, got %q", b.String())
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
	// No allowlist, or a container backend → no advisory.
	for _, rt := range []config.Runtime{
		{Backend: "microvm", Proxy: "http://p:3128"},
		{Backend: "docker", Proxy: "http://p:3128", Domains: []string{"a.com"}},
	} {
		b.Reset()
		warnMicrovmProxy(&b, rt)
		if b.Len() != 0 {
			t.Errorf("unexpected advisory for %+v: %q", rt, b.String())
		}
	}
}
