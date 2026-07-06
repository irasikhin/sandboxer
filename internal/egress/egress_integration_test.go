//go:build integration

package egress

import (
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/itest"
)

// TestUpDown_RealNetworks brings the egress allowlist up against a REAL engine:
// two networks plus the squid proxy sidecar (config.ProxyImage), verifies they
// exist, then tears it all down and verifies they are gone. Needs the proxy
// image (the sidecar runs it — the sandboxer binary is never in the net path).
func TestUpDown_RealNetworks(t *testing.T) {
	engine := itest.Engine(t)
	itest.EnsureProxyImage(t, engine)
	slug := itest.Slug("updown")

	e, err := Up(engine, slug, []string{"example.com"}, "", "", io.Discard)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	t.Cleanup(e.Down) // belt-and-suspenders even if an assertion below fails

	if !e.Active() {
		t.Fatal("egress not active after Up")
	}
	if nets := engineOut(t, engine, "network", "ls", "--format", "{{.Name}}"); !strings.Contains(nets, e.Net()) {
		t.Errorf("internal net %q not listed:\n%s", e.Net(), nets)
	}
	proxyName := strings.TrimSuffix(strings.TrimPrefix(e.ProxyURL(), "http://"), ":8888")
	if ps := engineOut(t, engine, "ps", "--format", "{{.Names}}"); !strings.Contains(ps, proxyName) {
		t.Errorf("proxy container %q not running:\n%s", proxyName, ps)
	}

	e.Down()
	if e.Active() {
		t.Error("Active() reports true after Down")
	}
	if nets := engineOut(t, engine, "network", "ls", "--format", "{{.Name}}"); strings.Contains(nets, e.Net()) {
		t.Errorf("internal net survived Down:\n%s", nets)
	}
}

// TestUp_RealEngine_TearsDownOnSidecarFailure: with a bogus sidecar image the
// proxy cannot start, so Up must fail AND remove the two networks it created
// (the real-engine counterpart of the fake TestUpProxyFailureTearsDown). Needs
// only an engine — the bogus SANDBOXER_PROXY_IMAGE is never pulled.
func TestUp_RealEngine_TearsDownOnSidecarFailure(t *testing.T) {
	engine := itest.Engine(t)
	slug := itest.Slug("sidecarfail")
	t.Setenv("SANDBOXER_PROXY_IMAGE", "sandboxer-bogus-image:does-not-exist")

	e, err := Up(engine, slug, []string{"example.com"}, "", "", io.Discard)
	if err == nil {
		t.Fatal("Up should fail when the sidecar image is missing")
	}
	if e != nil {
		t.Errorf("Up returned a non-nil Egress on failure: %+v", e)
	}
	// No network carrying our slug must remain after the self-teardown.
	if nets := engineOut(t, engine, "network", "ls", "--format", "{{.Name}}"); strings.Contains(nets, slug) {
		t.Errorf("networks for slug %q survived a failed Up:\n%s", slug, nets)
	}
}

func engineOut(t *testing.T, engine string, args ...string) string {
	t.Helper()
	out, err := exec.Command(engine, args...).Output()
	if err != nil {
		t.Fatalf("%s %s: %v", engine, strings.Join(args, " "), err)
	}
	return string(out)
}
