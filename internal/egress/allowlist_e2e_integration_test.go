//go:build integration

package egress

import (
	"bytes"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/itest"
)

// The allowlist e2e gates REAL outbound egress, which is exactly the proxy's
// job: an allowed host is reachable through the proxy, a blocked one is refused.
// example.com is a stable host that answers 200 over plain HTTP; blocked.test is
// never allowed. Needs outbound network access from the test host.
const (
	allowDomain = "example.com"
	blockDomain = "blocked.test"
)

// clientProbe runs inside the agent container. It retries the ALLOWED host while
// the proxy comes up, then tries the BLOCKED host once, printing a marker for
// each. The client sits on an --internal network with no route out, so a
// successful ALLOW can only have travelled THROUGH the proxy.
const clientProbe = `ok=ALLOW_FAIL
for i in $(seq 1 40); do if wget -q -O /dev/null http://example.com/; then ok=ALLOW_OK; break; fi; sleep 0.25; done
echo $ok
if wget -q -O /dev/null http://blocked.test/ 2>/dev/null; then echo BLOCK_REACHED; else echo BLOCK_DENIED; fi`

// assertAllowVsBlock runs the client probe on clientNet through proxyURL and
// asserts the allowed host was reached and the blocked host denied.
func assertAllowVsBlock(t *testing.T, engine, image, clientNet, proxyURL string) {
	t.Helper()
	var out bytes.Buffer
	cmd := exec.Command(engine, "run", "--rm", "--network", clientNet,
		"-e", "http_proxy="+proxyURL, "-e", "https_proxy="+proxyURL,
		image, "sh", "-c", clientProbe)
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("client run: %v\n%s", err, out.String())
	}
	got := out.String()
	if !strings.Contains(got, "ALLOW_OK") {
		t.Errorf("allowed host %q was not reachable through the proxy (offline host?):\n%s", allowDomain, got)
	}
	if !strings.Contains(got, "BLOCK_DENIED") || strings.Contains(got, "BLOCK_REACHED") {
		t.Errorf("blocked host %q was not denied by the proxy:\n%s", blockDomain, got)
	}
}

// TestEgressAllowlist_AllowVsBlock_RealSidecar is the allow/deny assertion
// driven through the real egress.Up sidecar (toolbox image). Needs the toolbox
// image for the sidecar and a smoke image for the client.
func TestEgressAllowlist_AllowVsBlock_RealSidecar(t *testing.T) {
	itest.RequireLiveEgress(t) // needs a container to reach the allowlisted host
	engine := itest.Engine(t)
	itest.EnsureProxyImage(t, engine)
	smoke := itest.SmokeImage(t, engine)
	slug := itest.Slug("allow")

	e, err := Up(engine, slug, []string{allowDomain}, "", "", io.Discard)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	t.Cleanup(e.Down)

	assertAllowVsBlock(t, engine, smoke, e.Net(), e.ProxyURL())
}
