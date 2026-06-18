package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/backend"
	"github.com/irasikhin/sandboxer/internal/config"
)

// seamCall records one (engine, slug, baseDir) lifecycle-seam invocation.
type seamCall struct{ engine, slug, baseDir string }

// stubStopSession replaces the stop seam with a recorder returning err.
func stubStopSession(t *testing.T, err error) *[]seamCall {
	t.Helper()
	calls := &[]seamCall{}
	old := backendStopSession
	t.Cleanup(func() { backendStopSession = old })
	backendStopSession = func(engine, slug, baseDir string) error {
		*calls = append(*calls, seamCall{engine, slug, baseDir})
		return err
	}
	return calls
}

// TestStopHappyPath: stop resolves the target like rm and stops its session
// container via the seam; backend.StopSession is idempotent, so a session
// that was never started (missing container) takes the exact same path.
func TestStopHappyPath(t *testing.T) {
	project := sessionProject(t)
	// Pin the engine so the resolved seam value does not depend on whether the
	// host happens to have docker on PATH (fakePodman only fakes podman).
	t.Setenv("SANDBOXER_ENGINE", "docker")
	calls := stubStopSession(t, nil)

	code, out, errs := run("stop", "feat", "--src", project)
	if code != 0 {
		t.Fatalf("stop = %d, %s", code, errs)
	}
	wantBase := config.StateDir(project)
	if len(*calls) != 1 || (*calls)[0] != (seamCall{"docker", "feat", wantBase}) {
		t.Errorf("stop calls = %+v, want [docker feat %s]", *calls, wantBase)
	}
	name := backend.SessionName("feat", wantBase)
	if !strings.Contains(out, name) || !strings.Contains(out, "sandboxer enter feat") {
		t.Errorf("stop output = %q, want the container name and the resume hint", out)
	}
}

// TestStopFailureSurfaces: an engine error stopping the session exits 1.
func TestStopFailureSurfaces(t *testing.T) {
	project := sessionProject(t)
	stubStopSession(t, errors.New("engine on fire"))

	code, _, errs := run("stop", "feat", "--src", project)
	if code != 1 || !strings.Contains(errs, "engine on fire") {
		t.Errorf("stop failure = (%d, %q), want exit 1 with the engine error", code, errs)
	}
}

// TestStopNoSandbox: with nothing to act on, stop fails before any seam call.
func TestStopNoSandbox(t *testing.T) {
	project := newProject(t)
	fakePodman(t)
	calls := stubStopSession(t, nil)

	code, _, errs := run("stop", "--src", project)
	if code != 1 || !strings.Contains(errs, "no sandbox selected") {
		t.Errorf("stop without sandbox = (%d, %q)", code, errs)
	}
	if len(*calls) != 0 {
		t.Errorf("no target, no seam call; got %+v", *calls)
	}
}

// TestStopEngineLessHost: unlike rm/rm-all (which degrade to a warning so the
// files still go), stop has nothing useful to do without an engine — it
// hard-fails with the engine diagnostic before any seam call. Deliberate
// asymmetry, pinned here.
func TestStopEngineLessHost(t *testing.T) {
	project := newProject(t)
	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}
	calls := stubStopSession(t, nil)
	t.Setenv("PATH", "") // no podman/docker discoverable
	t.Setenv("SANDBOXER_ENGINE", "")

	code, _, errs := run("stop", "feat", "--src", project)
	if code != 1 || !strings.Contains(errs, "docker or podman") {
		t.Errorf("engine-less stop = (%d, %q), want exit 1 with the engine hint", code, errs)
	}
	if len(*calls) != 0 {
		t.Errorf("no engine, no seam call; got %+v", *calls)
	}
}

// TestStopInvalidBackend: stop validates the backend like every sibling that
// resolves an engine — `--backend native` gets the explicit removal notice,
// not a silent fallback to the auto-detected engine.
func TestStopInvalidBackend(t *testing.T) {
	project := sessionProject(t)
	calls := stubStopSession(t, nil)

	code, _, errs := run("stop", "feat", "--src", project, "--backend", "native")
	if code != 1 || !strings.Contains(errs, "native backend was removed") {
		t.Errorf("stop --backend native = (%d, %q)", code, errs)
	}
	if len(*calls) != 0 {
		t.Errorf("invalid backend, no seam call; got %+v", *calls)
	}
}

// TestStopBlockedInContainer: stop joins the mutating-command blocklist.
func TestStopBlockedInContainer(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "1")
	code, _, errs := run("stop")
	if code != 1 || !strings.Contains(errs, "not available inside the sandbox") {
		t.Errorf("in-container stop = (%d, %q)", code, errs)
	}
}
