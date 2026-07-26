package cli

import (
	"bytes"
	"errors"
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
	if err := runSetup(&target{base: base, slug: "s", profile: nil}, config.Runtime{}, "", backend.NestedIDFiles{}, false, &buf); err != nil {
		t.Fatalf("nil profile: %v", err)
	}
	// empty setup → not pending.
	if err := runSetup(&target{base: base, slug: "s", profile: &config.Profile{}}, config.Runtime{}, "", backend.NestedIDFiles{}, false, &buf); err != nil {
		t.Fatalf("empty setup: %v", err)
	}
	// pending + --no-setup → skip without running.
	tp := &target{base: base, slug: "s", profile: &config.Profile{Setup: "npm ci"}}
	if err := runSetup(tp, config.Runtime{}, "", backend.NestedIDFiles{}, true, &buf); err != nil {
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
	if err := runSetup(tp, config.Runtime{}, "podman", backend.NestedIDFiles{}, false, &buf); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if len(gotArgs) != 3 || gotArgs[0] != "bash" || gotArgs[1] != "-lc" || gotArgs[2] != "make build" {
		t.Errorf("setup argv = %v", gotArgs)
	}
	if p, _ := base.SetupPending("s", "make build"); p {
		t.Error("setup must be stamped done after a clean run")
	}

	called := false
	backendRun = func(o backend.RunOpts) (int, error) { called = true; return 0, nil }
	if err := runSetup(tp, config.Runtime{}, "podman", backend.NestedIDFiles{}, false, &buf); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("stamped setup must not run again")
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
	if err := runSetup(tp, config.Runtime{}, "podman", backend.NestedIDFiles{}, false, &buf); err == nil {
		t.Error("non-zero setup exit must error")
	}
	if p, _ := base.SetupPending("s", "false"); !p {
		t.Error("failed setup must stay pending")
	}

	backendRun = func(o backend.RunOpts) (int, error) { return 0, errors.New("boom") }
	if err := runSetup(tp, config.Runtime{}, "podman", backend.NestedIDFiles{}, false, &buf); err == nil {
		t.Error("failed-to-start must error")
	}
}

// TestPrepareNestedIDs covers the generation gate: only a nestedContainers
// profile generates; a host without subordinate ranges comes back empty with
// the warning only where multi-uid WOULD have worked (podman engine); and a
// generation failure is a notice, never a fatal — the sandbox still enters,
// single-uid.
func TestPrepareNestedIDs(t *testing.T) {
	base, err := sandbox.ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func(old func(*sandbox.Base, string) (bool, error)) { sandboxWriteNestedIDs = old }(sandboxWriteNestedIDs)

	var buf bytes.Buffer
	called := false
	sandboxWriteNestedIDs = func(b *sandbox.Base, slug string) (bool, error) { called = true; return true, nil }

	// Profiles that did not opt in never generate.
	for _, p := range []*config.Profile{nil, {}} {
		if got := prepareNestedIDs(&target{base: base, slug: "s", profile: p}, "podman", &buf); got != (backend.NestedIDFiles{}) {
			t.Errorf("profile %+v = %+v, want zero", p, got)
		}
	}
	if called {
		t.Error("generation ran for a profile that did not opt in")
	}

	tp := &target{base: base, slug: "s", profile: &config.Profile{NestedContainers: true}}

	// Ranges found: the four _meta paths come back for RunOpts.
	if got, want := prepareNestedIDs(tp, "podman", &buf), backend.NestedIDFiles(base.NestedIDFiles("s")); got != want {
		t.Errorf("prepareNestedIDs = %+v, want %+v", got, want)
	}
	if buf.Len() != 0 {
		t.Errorf("successful generation must be silent, got %q", buf.String())
	}

	// No ranges on a podman engine: empty, and the actionable warning.
	sandboxWriteNestedIDs = func(b *sandbox.Base, slug string) (bool, error) { return false, nil }
	if got := prepareNestedIDs(tp, "podman", &buf); got != (backend.NestedIDFiles{}) {
		t.Errorf("no-ranges = %+v, want zero", got)
	}
	if !strings.Contains(buf.String(), "subordinate uid/gid ranges") {
		t.Errorf("expected the no-ranges warning, got %q", buf.String())
	}

	// Same on docker: empty but SILENT — multi-uid was never on offer there.
	buf.Reset()
	if got := prepareNestedIDs(tp, "docker", &buf); got != (backend.NestedIDFiles{}) {
		t.Errorf("docker no-ranges = %+v, want zero", got)
	}
	if buf.Len() != 0 {
		t.Errorf("docker engine must not warn about host subuids, got %q", buf.String())
	}

	// Generation failure: a notice, an empty set, no error escapes.
	buf.Reset()
	sandboxWriteNestedIDs = func(b *sandbox.Base, slug string) (bool, error) { return false, errors.New("disk full") }
	if got := prepareNestedIDs(tp, "podman", &buf); got != (backend.NestedIDFiles{}) {
		t.Errorf("failed generation = %+v, want zero", got)
	}
	if !strings.Contains(buf.String(), "disk full") {
		t.Errorf("expected the failure notice, got %q", buf.String())
	}
}
