package cli

import (
	"errors"
	"strings"
	"testing"
)

// stubSessionOrphans replaces doctor's orphan-enumeration seam.
func stubSessionOrphans(t *testing.T, orphans []string, err error) {
	t.Helper()
	old := sessionOrphans
	t.Cleanup(func() { sessionOrphans = old })
	sessionOrphans = func(engine string) ([]string, error) { return orphans, err }
}

// doctorEnv gives doctor an engine and an isolated profile store/cwd.
func doctorEnv(t *testing.T) {
	t.Helper()
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	t.Setenv("SANDBOXER_PROFILES", t.TempDir())
	fakePodman(t)
	t.Chdir(t.TempDir())
}

// TestDoctorSessions: with an engine present doctor tallies this project's
// sessions and warns about orphaned containers with a removal hint.
func TestDoctorSessions(t *testing.T) {
	doctorEnv(t)
	stubSessionStates(t, map[string]string{"a": "running", "b": "exited", "c": "created"}, nil)
	stubSessionOrphans(t, []string{"sandboxer-x-12345678"}, nil)

	code, out, _ := run("doctor")
	if code != 0 {
		t.Fatalf("doctor = %d", code)
	}
	if !strings.Contains(out, "1 running / 2 stopped for this project") {
		t.Errorf("doctor missing the session tally:\n%s", out)
	}
	for _, want := range []string{"orphan sessions", "sandboxer-x-12345678", "podman rm -f"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor orphan row missing %q:\n%s", want, out)
		}
	}
}

// TestDoctorSessionsClean: no sessions and no orphans — a zero tally and no
// orphan row.
func TestDoctorSessionsClean(t *testing.T) {
	doctorEnv(t)
	stubSessionStates(t, map[string]string{}, nil)
	stubSessionOrphans(t, nil, nil)

	code, out, _ := run("doctor")
	if code != 0 {
		t.Fatalf("doctor = %d", code)
	}
	if !strings.Contains(out, "0 running / 0 stopped for this project") {
		t.Errorf("doctor missing the zero tally:\n%s", out)
	}
	if strings.Contains(out, "orphan") {
		t.Errorf("clean doctor must not report orphans:\n%s", out)
	}
}

// TestDoctorSessionsProbeFailure: a failing enumeration becomes a warning row
// (and the advisory orphan probe stays silent), never a doctor failure.
func TestDoctorSessionsProbeFailure(t *testing.T) {
	doctorEnv(t)
	stubSessionStates(t, nil, errors.New("engine on fire"))
	stubSessionOrphans(t, []string{"never-reached"}, nil)

	code, out, _ := run("doctor")
	if code != 0 {
		t.Fatalf("doctor = %d", code)
	}
	if !strings.Contains(out, "engine on fire") {
		t.Errorf("doctor missing the probe warning:\n%s", out)
	}
	if strings.Contains(out, "never-reached") {
		t.Errorf("orphans must not be probed after a failing tally:\n%s", out)
	}
}
