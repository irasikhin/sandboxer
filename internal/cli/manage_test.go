package cli

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// stubRemoveSession replaces the rm seam with a recorder returning err. The
// recorder also snapshots whether the sandbox dir still existed at call time,
// pinning the container-before-files ordering.
func stubRemoveSession(t *testing.T, dest string, err error) (calls *[]seamCall, dirExisted *bool) {
	t.Helper()
	calls, dirExisted = &[]seamCall{}, new(bool)
	old := backendRemoveSession
	t.Cleanup(func() { backendRemoveSession = old })
	backendRemoveSession = func(engine, slug, baseDir string) error {
		*calls = append(*calls, seamCall{engine, slug, baseDir})
		*dirExisted = fileExists(dest)
		return err
	}
	return calls, dirExisted
}

// TestRmRemovesSessionBeforeFiles: rm tears the session container down while
// the sandbox files (the base dir its labels point at) still exist, then
// removes the files.
func TestRmRemovesSessionBeforeFiles(t *testing.T) {
	project := sessionProject(t)
	dest := filepath.Join(project, ".sandboxer", "feat")
	calls, dirExisted := stubRemoveSession(t, dest, nil)

	code, out, errs := run("rm", "feat", "--src", project)
	if code != 0 || !strings.Contains(out, "removed sandbox") {
		t.Fatalf("rm = (%d, %q, %q)", code, out, errs)
	}
	wantBase := filepath.Join(project, ".sandboxer")
	if len(*calls) != 1 || (*calls)[0] != (seamCall{"podman", "feat", wantBase}) {
		t.Errorf("rm session calls = %+v, want [podman feat %s]", *calls, wantBase)
	}
	if !*dirExisted {
		t.Error("the session must be removed BEFORE the sandbox files")
	}
	if fileExists(dest) {
		t.Error("sandbox files were not removed")
	}
}

// TestRmSessionFailureOnlyWarns: a failing session teardown must not block
// the file removal — one warning line, exit 0.
func TestRmSessionFailureOnlyWarns(t *testing.T) {
	project := sessionProject(t)
	dest := filepath.Join(project, ".sandboxer", "feat")
	stubRemoveSession(t, dest, errors.New("engine on fire"))

	code, out, errs := run("rm", "feat", "--src", project)
	if code != 0 || !strings.Contains(out, "removed sandbox") {
		t.Fatalf("rm with failing session cleanup = (%d, %q, %q)", code, out, errs)
	}
	if !strings.Contains(errs, "session cleanup failed") {
		t.Errorf("missing cleanup warning on stderr: %q", errs)
	}
	if fileExists(dest) {
		t.Error("sandbox files were not removed")
	}
}

// TestRmEngineLessHost: with no engine installed the files are still removed;
// the skipped session cleanup is a one-line warning, never a failure.
func TestRmEngineLessHost(t *testing.T) {
	project := newProject(t)
	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}
	dest := filepath.Join(project, ".sandboxer", "feat")
	calls, _ := stubRemoveSession(t, dest, nil)
	t.Setenv("PATH", "") // no podman/docker discoverable
	t.Setenv("SANDBOXER_ENGINE", "")

	code, out, errs := run("rm", "feat", "--src", project)
	if code != 0 || !strings.Contains(out, "removed sandbox") {
		t.Fatalf("engine-less rm = (%d, %q, %q)", code, out, errs)
	}
	if len(*calls) != 0 {
		t.Errorf("no engine, no session call; got %+v", *calls)
	}
	if !strings.Contains(errs, "session cleanup skipped") {
		t.Errorf("missing skip warning on stderr: %q", errs)
	}
	if fileExists(dest) {
		t.Error("sandbox files were not removed")
	}
}

// stubRemoveAllSessions replaces the rm-all seam with a recorder returning
// err, snapshotting whether the state dir still existed at call time.
func stubRemoveAllSessions(t *testing.T, stateDir string, err error) (calls *[]seamCall, dirExisted *bool) {
	t.Helper()
	calls, dirExisted = &[]seamCall{}, new(bool)
	old := backendRemoveAllSessions
	t.Cleanup(func() { backendRemoveAllSessions = old })
	backendRemoveAllSessions = func(engine, baseDir string) error {
		*calls = append(*calls, seamCall{engine: engine, baseDir: baseDir})
		*dirExisted = fileExists(stateDir)
		return err
	}
	return calls, dirExisted
}

// TestRmAllRemovesSessions: rm-all sweeps the project's session containers
// (labeled with the state dir) before deleting the state dir itself.
func TestRmAllRemovesSessions(t *testing.T) {
	project := sessionProject(t)
	stateDir := filepath.Join(project, ".sandboxer")
	calls, dirExisted := stubRemoveAllSessions(t, stateDir, nil)

	code, out, errs := run("rm-all", "--force", project)
	if code != 0 || !strings.Contains(out, "removed") {
		t.Fatalf("rm-all = (%d, %q, %q)", code, out, errs)
	}
	if len(*calls) != 1 || (*calls)[0] != (seamCall{engine: "podman", baseDir: stateDir}) {
		t.Errorf("rm-all session calls = %+v, want [podman %s]", *calls, stateDir)
	}
	if !*dirExisted {
		t.Error("the sessions must be removed BEFORE the state dir")
	}
	if fileExists(stateDir) {
		t.Error("state dir was not removed")
	}
}

// TestRmAllSessionFailureOnlyWarns: rm-all stays best-effort about the
// containers — a failing sweep warns but the state dir still goes.
func TestRmAllSessionFailureOnlyWarns(t *testing.T) {
	project := sessionProject(t)
	stateDir := filepath.Join(project, ".sandboxer")
	stubRemoveAllSessions(t, stateDir, errors.New("engine on fire"))

	code, out, errs := run("rm-all", "--force", project)
	if code != 0 || !strings.Contains(out, "removed") {
		t.Fatalf("rm-all with failing sweep = (%d, %q, %q)", code, out, errs)
	}
	if !strings.Contains(errs, "session cleanup failed") {
		t.Errorf("missing cleanup warning on stderr: %q", errs)
	}
	if fileExists(stateDir) {
		t.Error("state dir was not removed")
	}
}
