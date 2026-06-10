package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
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

// stubInstalledEngines pins the engine enumeration the sweeps/reports iterate,
// so the tests stay deterministic on hosts with a real docker beside the fake
// podman.
func stubInstalledEngines(t *testing.T, engines []string) {
	t.Helper()
	old := backendInstalledEngines
	t.Cleanup(func() { backendInstalledEngines = old })
	backendInstalledEngines = func(config.Defaults) []string { return engines }
}

// TestRmAllRemovesSessions: rm-all sweeps the project's session containers
// (labeled with the state dir) before deleting the state dir itself.
func TestRmAllRemovesSessions(t *testing.T) {
	project := sessionProject(t)
	stubInstalledEngines(t, []string{"podman"})
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
	stubInstalledEngines(t, []string{"podman"})
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

// TestRmAllSweepsEveryEngine: with both podman and docker installed the sweep
// must visit BOTH — sessions created via a profile's `backend: docker` would
// otherwise be stranded forever once the state dir is gone.
func TestRmAllSweepsEveryEngine(t *testing.T) {
	project := sessionProject(t)
	stubInstalledEngines(t, []string{"podman", "docker"})
	stateDir := filepath.Join(project, ".sandboxer")
	calls, _ := stubRemoveAllSessions(t, stateDir, nil)

	code, out, errs := run("rm-all", "--force", project)
	if code != 0 || !strings.Contains(out, "removed") {
		t.Fatalf("rm-all = (%d, %q, %q)", code, out, errs)
	}
	want := []seamCall{
		{engine: "podman", baseDir: stateDir},
		{engine: "docker", baseDir: stateDir},
	}
	if len(*calls) != 2 || (*calls)[0] != want[0] || (*calls)[1] != want[1] {
		t.Errorf("rm-all session calls = %+v, want %+v", *calls, want)
	}
}

// TestRmAllEngineLessHost: with no engine installed the state dir is still
// removed; the skipped sweep is a one-line warning, never a failure — the
// rm-all twin of TestRmEngineLessHost.
func TestRmAllEngineLessHost(t *testing.T) {
	project := newProject(t)
	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}
	stateDir := filepath.Join(project, ".sandboxer")
	calls, _ := stubRemoveAllSessions(t, stateDir, nil)
	t.Setenv("PATH", "") // no podman/docker discoverable
	t.Setenv("SANDBOXER_ENGINE", "")

	code, out, errs := run("rm-all", "--force", project)
	if code != 0 || !strings.Contains(out, "removed") {
		t.Fatalf("engine-less rm-all = (%d, %q, %q)", code, out, errs)
	}
	if len(*calls) != 0 {
		t.Errorf("no engine, no sweep call; got %+v", *calls)
	}
	if !strings.Contains(errs, "session cleanup skipped") {
		t.Errorf("missing skip warning on stderr: %q", errs)
	}
	if fileExists(stateDir) {
		t.Error("state dir was not removed")
	}
}

// TestRmRuntimeErrorOnlyWarns: a profile whose runtime cannot be resolved
// (invalid domain) skips the session cleanup with a warning — the files are
// still removed.
func TestRmRuntimeErrorOnlyWarns(t *testing.T) {
	project := newProject(t)
	fakePodman(t)
	cfg := filepath.Join(t.TempDir(), "p.yaml")
	profile := "name: feat\nnetwork:\n  allowedDomains: [\"not a domain\"]\n"
	if err := os.WriteFile(cfg, []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	// create fails resolving the runtime, but only AFTER the sandbox files and
	// the profile snapshot are written — exactly the broken state rm must cope with.
	if code, _, _ := run("create", "--src", project, "--config", cfg); code != 1 {
		t.Fatalf("create with a broken profile should fail resolving the runtime")
	}
	dest := filepath.Join(project, ".sandboxer", "feat")
	calls, _ := stubRemoveSession(t, dest, nil)

	code, out, errs := run("rm", "feat", "--src", project)
	if code != 0 || !strings.Contains(out, "removed sandbox") {
		t.Fatalf("rm with a broken profile = (%d, %q, %q)", code, out, errs)
	}
	if len(*calls) != 0 {
		t.Errorf("no runtime, no session call; got %+v", *calls)
	}
	if !strings.Contains(errs, "session cleanup skipped") {
		t.Errorf("missing skip warning on stderr: %q", errs)
	}
	if fileExists(dest) {
		t.Error("sandbox files were not removed")
	}
}
