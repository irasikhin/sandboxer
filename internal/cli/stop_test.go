package cli

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/backend"
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
	calls := stubStopSession(t, nil)

	code, out, errs := run("stop", "feat", "--src", project)
	if code != 0 {
		t.Fatalf("stop = %d, %s", code, errs)
	}
	wantBase := filepath.Join(project, ".sandboxer")
	if len(*calls) != 1 || (*calls)[0] != (seamCall{"podman", "feat", wantBase}) {
		t.Errorf("stop calls = %+v, want [podman feat %s]", *calls, wantBase)
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

// TestStopBlockedInContainer: stop joins the mutating-command blocklist.
func TestStopBlockedInContainer(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "1")
	code, _, errs := run("stop")
	if code != 1 || !strings.Contains(errs, "not available inside the container") {
		t.Errorf("in-container stop = (%d, %q)", code, errs)
	}
}
