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

// These tests exercise the REAL squid sidecar's enforcement behaviour end to end
// — the parts the fake-engine unit tests (TestSquidConf, TestUpWithUpstream)
// cannot see because they only inspect the generated conf/argv. A client sits on
// the `--internal` network with no route out, so anything it reaches can only
// have travelled THROUGH the proxy. allowDomain/blockDomain come from
// allowlist_e2e_integration_test.go (same package). Need the proxy image and a
// pre-pulled smoke image, plus outbound network from the test host.

// probeScript retries the reach URL ($1) while the proxy warms up, then makes a
// single bounded attempt at the block URL ($2), printing a marker for each. A
// per-try 6s timeout bounds the "denied"/"no route" cases.
const probeScript = `ok=REACH_FAIL
for i in $(seq 1 40); do if wget -q -T 6 -O /dev/null "$1"; then ok=REACH_OK; break; fi; sleep 0.25; done
echo $ok
if wget -q -T 6 -O /dev/null "$2" 2>/dev/null; then echo BLOCK_REACHED; else echo BLOCK_DENIED; fi`

// assertReachBlock runs the probe on clientNet through proxyURL and asserts that
// reachURL was fetched and blockURL was refused.
func assertReachBlock(t *testing.T, engine, image, clientNet, proxyURL, reachURL, blockURL string) {
	t.Helper()
	var out bytes.Buffer
	cmd := exec.Command(engine, "run", "--rm", "--network", clientNet,
		"-e", "http_proxy="+proxyURL, "-e", "https_proxy="+proxyURL,
		image, "sh", "-c", probeScript, "sh", reachURL, blockURL)
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("client run: %v\n%s", err, out.String())
	}
	got := out.String()
	if !strings.Contains(got, "REACH_OK") {
		t.Errorf("expected %q reachable through the proxy (offline host?):\n%s", reachURL, got)
	}
	if !strings.Contains(got, "BLOCK_DENIED") || strings.Contains(got, "BLOCK_REACHED") {
		t.Errorf("expected %q denied by the proxy:\n%s", blockURL, got)
	}
}

// upExampleAllow brings up the allowlist for example.com and registers teardown.
func upExampleAllow(t *testing.T, engine string) *Egress {
	t.Helper()
	e, err := Up(engine, itest.Slug("proxy"), []string{allowDomain}, "", nil, "", io.Discard)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	t.Cleanup(e.Down)
	return e
}

// TestProxy_SubdomainMatch_RealSidecar proves the leading-dot dstdomain rule the
// squid conf emits: allowing example.com also admits its subdomain
// www.example.com, while a non-listed domain is refused.
func TestProxy_SubdomainMatch_RealSidecar(t *testing.T) {
	itest.RequireLiveEgress(t) // needs a container to reach the allowlisted host
	engine := itest.Engine(t)
	itest.EnsureProxyImage(t, engine)
	smoke := itest.SmokeImage(t, engine)

	e := upExampleAllow(t, engine)
	assertReachBlock(t, engine, smoke, e.Net(), e.ProxyURL(),
		"http://www."+allowDomain+"/", "http://"+blockDomain+"/")
}

// HTTPS/CONNECT-to-443 is proven end to end by the CLI egress test
// (TestExec_Container_EgressOn_OneShot), which probes with curl from the toolbox
// image — busybox wget cannot tunnel HTTPS through a proxy, so it is the wrong
// client for that assertion here.

// TestProxy_NetworkIsolation_RealSidecar proves the sandbox has NO direct
// outbound: a client on the `--internal` network WITHOUT the proxy env cannot
// reach even the allowlisted host — the proxy is the only exit.
func TestProxy_NetworkIsolation_RealSidecar(t *testing.T) {
	engine := itest.Engine(t)
	itest.EnsureProxyImage(t, engine)
	smoke := itest.SmokeImage(t, engine)

	e := upExampleAllow(t, engine)
	// One bounded attempt with no proxy env; the internal net has no gateway, so
	// this must NOT reach the host (fails fast or times out). Markers are chosen
	// so neither is a substring of the other.
	var out bytes.Buffer
	cmd := exec.Command(engine, "run", "--rm", "--network", e.Net(),
		smoke, "sh", "-c",
		// `timeout` hard-caps the probe: -T alone does not bound a connect to a
		// routeless host, so without it a blocked attempt can hang for the OS SYN
		// timeout.
		`if timeout 8 wget -q -O /dev/null http://`+allowDomain+`/; then echo DIRECT_OK; else echo DIRECT_BLOCKED; fi`)
	cmd.Stdout, cmd.Stderr = &out, &out
	_ = cmd.Run()
	if strings.Contains(out.String(), "DIRECT_OK") {
		t.Errorf("allowed host reachable WITHOUT the proxy — the internal network is not isolated:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "DIRECT_BLOCKED") {
		t.Errorf("isolation probe produced no verdict (setup issue?):\n%s", out.String())
	}
}
