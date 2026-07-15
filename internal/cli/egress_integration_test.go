//go:build integration

package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/itest"
)

// TestExec_Container_EgressOn_OneShot drives the one-shot container path through
// the real CLI WITH the egress allowlist on — the combination the other CLI
// container tests skip (they all run SANDBOXER_NO_EGRESS=1). It proves that a
// one-shot `exec --ephemeral` stands up the squid sidecar, injects HTTP_PROXY
// into the container, and enforces the allowlist end to end: the allowed host is
// reachable and a non-listed host is refused. Needs the toolbox image (the
// sandbox + baked curl) and the squid proxy image (the egress sidecar), plus
// outbound network from the test host.
func TestExec_Container_EgressOn_OneShot(t *testing.T) {
	itest.RequireLiveEgress(t) // needs the sandbox container to reach the allowlisted host
	engine := itest.Engine(t)
	image := itest.EnsureToolboxImage(t, engine)
	itest.EnsureProxyImage(t, engine)
	t.Setenv("SANDBOXER_ENGINE", engine)
	t.Setenv("SANDBOXER_IMAGE", image)
	t.Setenv("SANDBOXER_NO_EGRESS", "") // egress ON
	t.Setenv("SANDBOXER_NO_AUTOBUILD", "1")

	project := newProject(t)
	t.Setenv("HOME", t.TempDir()) // no host creds bound in
	cfg := filepath.Join(t.TempDir(), "sbx.nix")
	body := "{ name = \"feat\"; backend = \"" + engine + "\"; srcs = [ { src = \".\"; } ]; " +
		"egress.allowedDomains = [ \"example.com\" ]; }\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	if code, out, errs := run("create", "--src", project, "--config", cfg); code != 0 {
		t.Fatalf("create = %d\nout: %s\nerr: %s", code, out, errs)
	}

	// The one-shot egress sidecar is PID-named (sbx-<slug>-<pid>); Run tears it
	// down on the way out, but register a belt-and-suspenders sweep.
	id := "sbx-feat-" + strconv.Itoa(os.Getpid())
	itest.CleanupNetwork(t, engine, id+"-int")
	itest.CleanupNetwork(t, engine, id+"-ext")
	itest.CleanupContainer(t, engine, id+"-proxy")

	// Probe from inside the sandbox: report the injected proxy env, then curl the
	// allowed host over HTTP and HTTPS (both must reach — the HTTPS leg proves
	// CONNECT to 443 works through squid) and a non-listed host (must be refused).
	probe := "echo PROXY=$HTTP_PROXY; " +
		"if curl -sf -m 12 -o /dev/null http://example.com/; then echo HTTP_OK; else echo HTTP_FAIL; fi; " +
		"if curl -sf -m 12 -o /dev/null https://example.com/; then echo HTTPS_OK; else echo HTTPS_FAIL; fi; " +
		"if curl -sf -m 8 -o /dev/null http://blocked.test/; then echo BLOCK_REACHED; else echo BLOCK_DENIED; fi"

	code, out, errs := run("exec", "feat", "--src", project, "--config", cfg,
		"--ephemeral", "--", "bash", "-c", probe)
	if code != 0 {
		t.Fatalf("exec = %d\nout: %s\nerr: %s", code, out, errs)
	}
	if !strings.Contains(out, "PROXY=http://") {
		t.Errorf("HTTP_PROXY was not injected into the sandbox:\nout: %s\nerr: %s", out, errs)
	}
	if !strings.Contains(out, "HTTP_OK") {
		t.Errorf("allowed host example.com not reachable over HTTP through the egress proxy:\nout: %s\nerr: %s", out, errs)
	}
	if !strings.Contains(out, "HTTPS_OK") {
		t.Errorf("allowed host example.com not reachable over HTTPS (CONNECT to 443) through the egress proxy:\nout: %s\nerr: %s", out, errs)
	}
	if !strings.Contains(out, "BLOCK_DENIED") || strings.Contains(out, "BLOCK_REACHED") {
		t.Errorf("non-listed host was not denied by the egress proxy:\nout: %s\nerr: %s", out, errs)
	}
}
