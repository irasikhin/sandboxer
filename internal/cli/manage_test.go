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
	// Pin the engine so the resolved seam value does not depend on whether the
	// host happens to have docker on PATH (fakePodman only fakes podman).
	t.Setenv("SANDBOXER_ENGINE", "docker")
	dest := stateDir(project, "feat")
	calls, dirExisted := stubRemoveSession(t, dest, nil)

	code, out, errs := run("rm", "feat", "--src", project)
	if code != 0 || !strings.Contains(out, "removed sandbox") {
		t.Fatalf("rm = (%d, %q, %q)", code, out, errs)
	}
	wantBase := config.StateDir(project)
	if len(*calls) != 1 || (*calls)[0] != (seamCall{"docker", "feat", wantBase}) {
		t.Errorf("rm session calls = %+v, want [docker feat %s]", *calls, wantBase)
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
	dest := stateDir(project, "feat")
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
	dest := stateDir(project, "feat")
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

// stubRemoveAllSessions replaces the clean seam with a recorder returning
// err, snapshotting whether the state dir still existed at call time.
func stubRemoveAllSessions(t *testing.T, sdir string, err error) (calls *[]seamCall, dirExisted *bool) {
	t.Helper()
	calls, dirExisted = &[]seamCall{}, new(bool)
	old := backendRemoveAllSessions
	t.Cleanup(func() { backendRemoveAllSessions = old })
	backendRemoveAllSessions = func(engine, baseDir string) error {
		*calls = append(*calls, seamCall{engine: engine, baseDir: baseDir})
		*dirExisted = fileExists(sdir)
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

// TestCleanRemovesSessions: clean sweeps the project's session containers
// (labeled with the state dir) before deleting the state dir itself.
func TestCleanRemovesSessions(t *testing.T) {
	project := sessionProject(t)
	stubInstalledEngines(t, []string{"podman"})
	sdir := config.StateDir(project)
	calls, dirExisted := stubRemoveAllSessions(t, sdir, nil)

	code, out, errs := run("clean", "--force", project)
	if code != 0 || !strings.Contains(out, "removed") {
		t.Fatalf("clean = (%d, %q, %q)", code, out, errs)
	}
	if len(*calls) != 1 || (*calls)[0] != (seamCall{engine: "podman", baseDir: sdir}) {
		t.Errorf("clean session calls = %+v, want [podman %s]", *calls, sdir)
	}
	if !*dirExisted {
		t.Error("the sessions must be removed BEFORE the state dir")
	}
	if fileExists(sdir) {
		t.Error("state dir was not removed")
	}
}

// TestCleanSessionFailureOnlyWarns: clean stays best-effort about the
// containers — a failing sweep warns but the state dir still goes.
func TestCleanSessionFailureOnlyWarns(t *testing.T) {
	project := sessionProject(t)
	stubInstalledEngines(t, []string{"podman"})
	sdir := config.StateDir(project)
	stubRemoveAllSessions(t, sdir, errors.New("engine on fire"))

	code, out, errs := run("clean", "--force", project)
	if code != 0 || !strings.Contains(out, "removed") {
		t.Fatalf("clean with failing sweep = (%d, %q, %q)", code, out, errs)
	}
	if !strings.Contains(errs, "session cleanup failed") {
		t.Errorf("missing cleanup warning on stderr: %q", errs)
	}
	if fileExists(sdir) {
		t.Error("state dir was not removed")
	}
}

// TestCleanSweepsEveryEngine: with both podman and docker installed the sweep
// must visit BOTH — sessions created via a profile's `backend: docker` would
// otherwise be stranded forever once the state dir is gone.
func TestCleanSweepsEveryEngine(t *testing.T) {
	project := sessionProject(t)
	stubInstalledEngines(t, []string{"podman", "docker"})
	sdir := config.StateDir(project)
	calls, _ := stubRemoveAllSessions(t, sdir, nil)

	code, out, errs := run("clean", "--force", project)
	if code != 0 || !strings.Contains(out, "removed") {
		t.Fatalf("clean = (%d, %q, %q)", code, out, errs)
	}
	want := []seamCall{
		{engine: "podman", baseDir: sdir},
		{engine: "docker", baseDir: sdir},
	}
	if len(*calls) != 2 || (*calls)[0] != want[0] || (*calls)[1] != want[1] {
		t.Errorf("clean session calls = %+v, want %+v", *calls, want)
	}
}

// TestCleanEngineLessHost: with no engine installed the state dir is still
// removed; the skipped sweep is a one-line warning, never a failure — the
// clean twin of TestRmEngineLessHost.
func TestCleanEngineLessHost(t *testing.T) {
	project := newProject(t)
	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}
	sdir := config.StateDir(project)
	calls, _ := stubRemoveAllSessions(t, sdir, nil)
	t.Setenv("PATH", "") // no podman/docker discoverable
	t.Setenv("SANDBOXER_ENGINE", "")

	code, out, errs := run("clean", "--force", project)
	if code != 0 || !strings.Contains(out, "removed") {
		t.Fatalf("engine-less clean = (%d, %q, %q)", code, out, errs)
	}
	if len(*calls) != 0 {
		t.Errorf("no engine, no sweep call; got %+v", *calls)
	}
	if !strings.Contains(errs, "session cleanup skipped") {
		t.Errorf("missing skip warning on stderr: %q", errs)
	}
	if fileExists(sdir) {
		t.Error("state dir was not removed")
	}
}

// TestRmRuntimeErrorOnlyWarns: a profile whose runtime cannot be resolved
// (invalid domain) skips the session cleanup with a warning — the files are
// still removed.
func TestRmRuntimeErrorOnlyWarns(t *testing.T) {
	project := newProject(t)
	fakePodman(t)
	cfg := filepath.Join(t.TempDir(), "p.nix")
	profile := "{ name = \"feat\"; egress.allowedDomains = [ \"not a domain\" ]; }\n"
	if err := os.WriteFile(cfg, []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	// create fails resolving the runtime, but only AFTER the sandbox files and
	// the profile snapshot are written — exactly the broken state rm must cope with.
	if code, _, _ := run("create", "--src", project, "--config", cfg); code != 1 {
		t.Fatalf("create with a broken profile should fail resolving the runtime")
	}
	dest := stateDir(project, "feat")
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
