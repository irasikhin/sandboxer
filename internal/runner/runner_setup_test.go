package runner

import (
	"io"
	"testing"

	"github.com/irasikhin/sandboxer/internal/backend"
	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/sandbox"
)

// TestLaunchSpecRunSetup covers the batch setup gate via the container-run seam:
// nothing-to-do, success-stamps-and-is-idempotent, non-zero-exit, failed-start,
// and the --no-setup skip — all without a real engine.
func TestLaunchSpecRunSetup(t *testing.T) {
	base, err := sandbox.ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func(old func(backend.RunOpts) (int, error)) { backendRun = old }(backendRun)

	// nil profile → nothing to do, no run.
	nilp := launchSpec{base: base, slug: "nilp", profile: nil, stderr: io.Discard}
	if rc := nilp.runSetup("/d", "/e", config.Runtime{}, io.Discard); rc != 0 {
		t.Fatalf("nil profile rc=%d", rc)
	}

	// success → stamps and returns 0.
	var ranArgs []string
	backendRun = func(o backend.RunOpts) (int, error) { ranArgs = o.Args; return 0, nil }
	ok := launchSpec{base: base, slug: "ok", engine: "podman", image: "img",
		profile: &config.Profile{Setup: "make"}, stderr: io.Discard}
	if rc := ok.runSetup("/d", "/e", config.Runtime{}, io.Discard); rc != 0 {
		t.Fatalf("success rc=%d", rc)
	}
	if len(ranArgs) != 3 || ranArgs[2] != "make" {
		t.Errorf("setup argv = %v", ranArgs)
	}
	if p, _ := base.SetupPending("ok", "make"); p {
		t.Error("clean setup must be stamped")
	}

	// idempotent: a stamped sandbox does not re-run.
	backendRun = func(o backend.RunOpts) (int, error) { t.Fatal("re-ran stamped setup"); return 0, nil }
	if rc := ok.runSetup("/d", "/e", config.Runtime{}, io.Discard); rc != 0 {
		t.Fatalf("idempotent rc=%d", rc)
	}

	// non-zero exit → rc 1, stays pending.
	backendRun = func(o backend.RunOpts) (int, error) { return 7, nil }
	fail := launchSpec{base: base, slug: "fail", engine: "p", image: "i",
		profile: &config.Profile{Setup: "false"}, stderr: io.Discard}
	if rc := fail.runSetup("/d", "/e", config.Runtime{}, io.Discard); rc != 1 {
		t.Errorf("non-zero exit rc=%d want 1", rc)
	}
	if p, _ := base.SetupPending("fail", "false"); !p {
		t.Error("failed setup must stay pending")
	}

	// failed to start → rc 1.
	backendRun = func(o backend.RunOpts) (int, error) { return 0, io.ErrClosedPipe }
	if rc := fail.runSetup("/d", "/e", config.Runtime{}, io.Discard); rc != 1 {
		t.Errorf("failed-start rc=%d want 1", rc)
	}

	// --no-setup → skip, no run.
	backendRun = func(o backend.RunOpts) (int, error) { t.Fatal("ran under --no-setup"); return 0, nil }
	skip := launchSpec{base: base, slug: "skip", profile: &config.Profile{Setup: "make"},
		noSetup: true, stderr: io.Discard}
	if rc := skip.runSetup("/d", "/e", config.Runtime{}, io.Discard); rc != 0 {
		t.Errorf("--no-setup rc=%d", rc)
	}
}
