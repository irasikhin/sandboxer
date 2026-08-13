package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/backend"
	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/sandbox"
)

// TestRunSetupEngineFreeBranches covers the decisions runSetup makes before it
// would ever launch a container: no profile, no setup script, and the
// --no-setup skip on a pending script. None of these reach backend.Run, so the
// test needs no engine.
func TestRunSetupEngineFreeBranches(t *testing.T) {
	base, err := sandbox.ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer

	// nil profile → nothing to do.
	if err := runSetup(&target{base: base, slug: "s", profile: nil}, config.Runtime{}, "", false, &buf); err != nil {
		t.Fatalf("nil profile: %v", err)
	}
	// empty setup → not pending.
	if err := runSetup(&target{base: base, slug: "s", profile: &config.Profile{}}, config.Runtime{}, "", false, &buf); err != nil {
		t.Fatalf("empty setup: %v", err)
	}
	// pending + --no-setup → skip without running.
	tp := &target{base: base, slug: "s", profile: &config.Profile{Setup: "npm ci"}}
	if err := runSetup(tp, config.Runtime{}, "", true, &buf); err != nil {
		t.Fatalf("no-setup skip: %v", err)
	}
	if !strings.Contains(buf.String(), "skipping setup") {
		t.Errorf("expected a skip notice, got %q", buf.String())
	}
	// The skip must NOT mark the stamp — setup is still pending for a later run.
	if p, _ := base.SetupPending("s", "npm ci"); !p {
		t.Error("--no-setup must not mark setup done")
	}
}

// TestRunSetupRunsStampsAndIsIdempotent stubs the container-run seam to cover
// the run → stamp → skip-on-rerun path without a real engine.
func TestRunSetupRunsStampsAndIsIdempotent(t *testing.T) {
	base, err := sandbox.ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func(old func(backend.RunOpts) (int, error)) { backendRun = old }(backendRun)

	var gotArgs []string
	backendRun = func(o backend.RunOpts) (int, error) { gotArgs = o.Args; return 0, nil }

	tp := &target{base: base, slug: "s", profile: &config.Profile{Setup: "make build"}}
	var buf bytes.Buffer
	if err := runSetup(tp, config.Runtime{}, "microsandbox", false, &buf); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if !slices.Equal(gotArgs, podmanSocketPrefix([]string{"bash", "-lc", "make build"})) {
		t.Errorf("setup argv = %v, want the podman-socket-wrapped bash -lc make build", gotArgs)
	}
	if p, _ := base.SetupPending("s", "make build"); p {
		t.Error("setup must be stamped done after a clean run")
	}

	called := false
	backendRun = func(o backend.RunOpts) (int, error) { called = true; return 0, nil }
	if err := runSetup(tp, config.Runtime{}, "microsandbox", false, &buf); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("stamped setup must not run again")
	}
}

// TestRunSetupLogsOutput: the script's output is tee'd into
// _logs/<slug>.setup.log so a failure that scrolled away stays debuggable,
// and the failure hint names the file.
func TestRunSetupLogsOutput(t *testing.T) {
	base, err := sandbox.ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func(old func(backend.RunOpts) (int, error)) { backendRun = old }(backendRun)
	backendRun = func(o backend.RunOpts) (int, error) {
		fmt.Fprintln(o.Stdout, "npm ERR! boom")
		return 3, nil
	}
	var buf bytes.Buffer

	tp := &target{base: base, slug: "s", profile: &config.Profile{Setup: "npm ci"}}
	serr := runSetup(tp, config.Runtime{}, "microsandbox", false, &buf)
	if serr == nil {
		t.Fatal("non-zero setup exit must error")
	}
	logPath := base.LogPath("s", "setup.log")
	if !strings.Contains(serr.Error(), logPath) {
		t.Errorf("setup error should name the saved log, got %q", serr)
	}
	data, rerr := os.ReadFile(logPath)
	if rerr != nil || !strings.Contains(string(data), "npm ERR! boom") {
		t.Errorf("setup log = (%q, %v), want the script output captured", data, rerr)
	}
	if !strings.Contains(buf.String(), "npm ERR! boom") {
		t.Error("the terminal must still see the script output")
	}
}

// TestRunSetupFailures covers the non-zero-exit and failed-to-start branches;
// neither stamps the sandbox, so setup stays pending.
func TestRunSetupFailures(t *testing.T) {
	base, err := sandbox.ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func(old func(backend.RunOpts) (int, error)) { backendRun = old }(backendRun)
	var buf bytes.Buffer

	backendRun = func(o backend.RunOpts) (int, error) { return 2, nil }
	tp := &target{base: base, slug: "s", profile: &config.Profile{Setup: "false"}}
	if err := runSetup(tp, config.Runtime{}, "microsandbox", false, &buf); err == nil {
		t.Error("non-zero setup exit must error")
	}
	if p, _ := base.SetupPending("s", "false"); !p {
		t.Error("failed setup must stay pending")
	}

	backendRun = func(o backend.RunOpts) (int, error) { return 0, errors.New("boom") }
	if err := runSetup(tp, config.Runtime{}, "microsandbox", false, &buf); err == nil {
		t.Error("failed-to-start must error")
	}
}
