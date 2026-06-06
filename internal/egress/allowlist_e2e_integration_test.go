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

// TestProxyInContainer_RealProxyBinary stands up the egress topology by hand —
// WITHOUT the toolbox image — using the host-built sandboxer binary as the
// `_proxy` sidecar. It is the real allowlist end-to-end test that runs on a
// plain docker/podman box: a client on the --internal network reaches the
// allowed host through the proxy and is denied the blocked one.
func TestProxyInContainer_RealProxyBinary(t *testing.T) {
	engine := itest.Engine(t)
	image := itest.SmokeImage(t, engine)
	bin := itest.BuildBinary(t)

	intNet := itest.Slug("sbx-e2e-int")
	extNet := itest.Slug("sbx-e2e-ext")
	proxy := itest.Slug("sbx-e2e-proxy")

	mustRun(t, engine, "network", "create", "--internal", intNet)
	itest.CleanupNetwork(t, engine, intNet)
	mustRun(t, engine, "network", "create", extNet)
	itest.CleanupNetwork(t, engine, extNet)

	// Proxy: the host-built sandboxer binary, on the internal net, also attached
	// to the external net so it (and only it) can reach the allowed host.
	itest.CleanupContainer(t, engine, proxy)
	mustRun(t, engine, "run", "-d", "--name", proxy, "--network", intNet,
		"-v", bin+":/sbx:ro", image,
		"/sbx", "_proxy", "--listen", ":8888", "--allow", allowDomain)
	mustRun(t, engine, "network", "connect", extNet, proxy)

	assertAllowVsBlock(t, engine, image, intNet, "http://"+proxy+":8888")
}

// TestEgressAllowlist_AllowVsBlock_RealSidecar is the same allow/deny assertion
// driven through the real egress.Up sidecar (toolbox image). Needs the toolbox
// image for the sidecar and a smoke image for the client.
func TestEgressAllowlist_AllowVsBlock_RealSidecar(t *testing.T) {
	engine := itest.Engine(t)
	toolbox := itest.EnsureToolboxImage(t, engine)
	smoke := itest.SmokeImage(t, engine)
	slug := itest.Slug("allow")

	e, err := Up(engine, toolbox, slug, []string{allowDomain}, io.Discard)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	t.Cleanup(e.Down)

	assertAllowVsBlock(t, engine, smoke, e.Net(), e.ProxyURL())
}

func mustRun(t *testing.T, engine string, args ...string) {
	t.Helper()
	var buf bytes.Buffer
	cmd := exec.Command(engine, args...)
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s %s: %v\n%s", engine, strings.Join(args, " "), err, buf.String())
	}
}
