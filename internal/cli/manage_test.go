package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/sandbox"
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
	dest := sandboxDir(project, "feat")
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
	dest := sandboxDir(project, "feat")
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
	dest := sandboxDir(project, "feat")
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

// TestRmByID: the id `list` prints is a HOST-WIDE handle — rm takes it (or an
// unambiguous prefix) from any directory and removes that sandbox in its own
// project, including a project whose directory is gone, which no cd and no
// --src can reach. Several may go at once, and every argument is resolved
// before anything is removed, so a typo removes nothing.
func TestRmByID(t *testing.T) {
	t.Setenv("SANDBOXER_STATE", t.TempDir()) // isolate the host-wide index
	here := sessionProject(t)                // the cwd project, with "feat"
	other := newProject(t)
	if code, _, errs := run("create", "away", "--src", other); code != 0 {
		t.Fatalf("create away: %d %s", code, errs)
	}
	deleted := newProject(t)
	if code, _, errs := run("create", "left", "--src", deleted); code != 0 {
		t.Fatalf("create left: %d %s", code, errs)
	}
	if err := os.RemoveAll(deleted); err != nil {
		t.Fatal(err)
	}
	awayID := sandbox.ID(config.StateDir(other), "away")
	leftID := sandbox.ID(config.StateDir(deleted), "left")
	stubRemoveSession(t, "", nil)
	t.Chdir(here)

	t.Run("an unknown id-shaped token removes nothing", func(t *testing.T) {
		code, _, errs := run("rm", "feat", "ffffffff")
		if code == 0 {
			t.Fatal("rm with an unknown id = 0, want a failure")
		}
		if !fileExists(sandboxDir(here, "feat")) {
			t.Errorf("a failed batch removed feat anyway (errs=%q)", errs)
		}
	})

	t.Run("a prefix reaches another project", func(t *testing.T) {
		code, out, errs := run("rm", awayID[:sandbox.MinIDPrefix])
		if code != 0 {
			t.Fatalf("rm by id prefix = %d, %s", code, errs)
		}
		// The project must be named: "removed sandbox: away" alone reads like
		// something in the cwd project just went.
		if !strings.Contains(out, "removed sandbox: away") || !strings.Contains(out, other) {
			t.Errorf("rm output = %q, want the slug AND the project %s", out, other)
		}
		if fileExists(sandboxDir(other, "away")) {
			t.Error("the sandbox in the other project survived")
		}
	})

	t.Run("a gone project's leftover can be cleared", func(t *testing.T) {
		code, out, errs := run("rm", leftID)
		if code != 0 {
			t.Fatalf("rm of a gone project's sandbox = %d, %s", code, errs)
		}
		if !strings.Contains(out, "removed sandbox: left") {
			t.Errorf("rm output = %q", out)
		}
		if _, err := sandbox.FindByID(leftID); !errors.Is(err, sandbox.ErrNoSuchID) {
			t.Errorf("the leftover is still in the host-wide index: %v", err)
		}
	})

	t.Run("an ambiguous prefix is an error, not a guess", func(t *testing.T) {
		// A second sandbox whose id shares the prefix. The ids are real hashes,
		// so the collision is searched for rather than declared.
		twin := collidingSlug(t, config.StateDir(here), sandbox.ID(config.StateDir(here), "feat"))
		base, err := sandbox.OpenBase(here)
		if err != nil || base == nil {
			t.Fatalf("OpenBase(%s): %v", here, err)
		}
		if err := base.AppendAgent(twin); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = base.RemoveAgent(twin) })

		prefix := sandbox.ID(config.StateDir(here), "feat")[:sandbox.MinIDPrefix]
		code, _, errs := run("rm", prefix)
		if code == 0 {
			t.Fatal("an ambiguous prefix removed something")
		}
		if !strings.Contains(errs, "ambiguous") {
			t.Errorf("rm %s: %q, want an ambiguity error naming the candidates", prefix, errs)
		}
	})

	t.Run("a slug of the current project wins over an id-shaped token", func(t *testing.T) {
		// "beefed" is hex and long enough to look like an id; as a sandbox of
		// the project we stand in it must still resolve as the slug it is.
		cfg := filepath.Join(t.TempDir(), "beefed.nix")
		if err := os.WriteFile(cfg, []byte(`{ name = "beefed"; srcs = [ { src = "."; branch = "feat/x"; } ]; }`), 0o644); err != nil {
			t.Fatal(err)
		}
		if code, _, errs := run("create", "--src", here, "--config", cfg); code != 0 {
			t.Fatalf("create beefed: %d %s", code, errs)
		}
		code, out, errs := run("rm", "beefed")
		if code != 0 {
			t.Fatalf("rm beefed = %d, %s", code, errs)
		}
		if strings.Contains(out, "(") { // no project suffix: it was the local one
			t.Errorf("rm output = %q, want the local sandbox with no project suffix", out)
		}
	})
}

// collidingSlug searches stateDir's id space for a slug whose id shares its
// first MinIDPrefix characters with want. The ids are sha256 prefixes, so an
// ambiguous prefix cannot be written down — it has to be found (one in 16^4
// tries, milliseconds).
func collidingSlug(t *testing.T, stateDir, want string) string {
	t.Helper()
	for i := 0; i < 1<<24; i++ {
		slug := fmt.Sprintf("twin%d", i)
		if strings.HasPrefix(sandbox.ID(stateDir, slug), want[:sandbox.MinIDPrefix]) {
			return slug
		}
	}
	t.Fatalf("no slug found whose id shares the prefix of %s", want)
	return ""
}

// TestRmRejectsUnsafeSlug: a slug of "." or ".." resolves the removal path onto
// the worktrees root (".") or the PROJECT ROOT ("..", via filepath.Join), where
// os.RemoveAll would wipe it. rm must refuse it and delete nothing — the guard
// for the path-traversal data-loss class (config.ValidSlug + the RemoveState
// containment check).
func TestRmRejectsUnsafeSlug(t *testing.T) {
	project := newProject(t)
	// A real sandbox so there is state that must survive the rejected rm.
	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}
	marker := filepath.Join(project, "f.txt") // planted by newProject
	worktree := sandboxDir(project, "feat")
	for _, bad := range []string{".", ".."} {
		code, _, errs := run("rm", bad, "--src", project)
		if code == 0 {
			t.Fatalf("rm %q: want a non-zero exit, got 0 (errs=%q)", bad, errs)
		}
		if !fileExists(marker) {
			t.Fatalf("rm %q deleted the project directory (marker %s is gone)", bad, marker)
		}
		if !fileExists(sandboxDir(project)) { // the worktrees root (no slug part)
			t.Fatalf("rm %q deleted the worktrees root", bad)
		}
		if !fileExists(worktree) {
			t.Fatalf("rm %q removed the sandbox worktree", bad)
		}
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
	old := backendSweepEngines
	t.Cleanup(func() { backendSweepEngines = old })
	backendSweepEngines = func(config.Defaults) []string { return engines }
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
	dest := sandboxDir(project, "feat")
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
