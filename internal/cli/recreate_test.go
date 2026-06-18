package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
)

// seedRecreateState drops marker files into a created sandbox: junk in the
// working copy, a credential in the private agent home and a setup stamp —
// the three classes of state recreate must treat differently.
func seedRecreateState(t *testing.T, project string) (junk, cred, stamp string) {
	t.Helper()
	junk = stateDir(project, "feat", "junk.txt")
	cred = stateDir(project, "_home", "feat", "cred.json")
	stamp = stateDir(project, "_meta", "feat.setup")
	for _, p := range []string{junk, cred, stamp} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return junk, cred, stamp
}

// TestRecreateKeepsAgentHome: the default recreate rebuilds the working copy
// (junk and setup stamp gone, deps re-pulled, snapshot restored) but preserves
// the private agent home, and tears the session container down BEFORE the
// files — the same ordering contract as rm.
func TestRecreateKeepsAgentHome(t *testing.T) {
	project := sessionProject(t)
	t.Setenv("SANDBOXER_ENGINE", "docker")
	dest := stateDir(project, "feat")
	calls, dirExisted := stubRemoveSession(t, dest, nil)
	junk, cred, stamp := seedRecreateState(t, project)

	code, out, errs := run("recreate", "feat", "--src", project)
	if code != 0 || !strings.Contains(out, "recreated") {
		t.Fatalf("recreate = (%d, %q, %q)", code, out, errs)
	}
	if strings.Contains(out, "re-authenticate") {
		t.Errorf("default recreate must not claim the home was wiped: %q", out)
	}
	if fileExists(junk) {
		t.Error("working copy was not rebuilt (junk survived)")
	}
	if fileExists(stamp) {
		t.Error("setup stamp survived — setup would not re-run")
	}
	if !fileExists(cred) {
		t.Error("agent home was wiped by a default recreate")
	}
	if !fileExists(stateDir(project, "_meta", "feat.profile.json")) {
		t.Error("profile snapshot was not restored")
	}
	wantBase := config.StateDir(project)
	if len(*calls) != 1 || (*calls)[0] != (seamCall{"docker", "feat", wantBase}) {
		t.Errorf("recreate session calls = %+v, want [docker feat %s]", *calls, wantBase)
	}
	if !*dirExisted {
		t.Error("the session must be removed BEFORE the sandbox files")
	}
}

// TestRecreateFullWipesHome: --full is rm+create — the agent home goes too,
// the output says so, and the registration (agents.list, active marker) is
// restored afterwards.
func TestRecreateFullWipesHome(t *testing.T) {
	project := sessionProject(t)
	dest := stateDir(project, "feat")
	stubRemoveSession(t, dest, nil)
	if code, _, errs := run("use", "feat", "--src", project); code != 0 {
		t.Fatalf("use: %s", errs)
	}
	_, cred, _ := seedRecreateState(t, project)

	code, out, errs := run("recreate", "feat", "--src", project, "--full")
	if code != 0 || !strings.Contains(out, "recreated") {
		t.Fatalf("recreate --full = (%d, %q, %q)", code, out, errs)
	}
	if !strings.Contains(out, "re-authenticate") {
		t.Errorf("--full must announce the wiped home: %q", out)
	}
	if fileExists(cred) {
		t.Error("agent home survived --full")
	}
	agents, err := os.ReadFile(stateDir(project, "_meta", "agents.list"))
	if err != nil || !strings.Contains(string(agents), "feat") {
		t.Errorf("sandbox not re-registered after --full: %q, %v", agents, err)
	}
	if code, out, _ := run("use", "--src", project); code != 0 || !strings.Contains(out, "feat") {
		t.Errorf("active sandbox not restored after --full: %q", out)
	}
}

// TestRecreateSessionFailureOnlyWarns: a failing session teardown must not
// block the rebuild — one warning line, exit 0.
func TestRecreateSessionFailureOnlyWarns(t *testing.T) {
	project := sessionProject(t)
	dest := stateDir(project, "feat")
	stubRemoveSession(t, dest, errors.New("engine on fire"))
	junk, _, _ := seedRecreateState(t, project)

	code, out, errs := run("recreate", "feat", "--src", project)
	if code != 0 || !strings.Contains(out, "recreated") {
		t.Fatalf("recreate with failing session cleanup = (%d, %q, %q)", code, out, errs)
	}
	if !strings.Contains(errs, "session cleanup failed") {
		t.Errorf("missing cleanup warning on stderr: %q", errs)
	}
	if fileExists(junk) {
		t.Error("working copy was not rebuilt")
	}
}

// TestRecreateNoProfile: recreating a sandbox that has no profile (live or
// stored) errors instead of silently making an empty one — recreate never
// auto-scaffolds.
func TestRecreateNoProfile(t *testing.T) {
	project := newProject(t)
	code, _, errs := run("recreate", "feat", "--src", project)
	if code != 1 || !strings.Contains(errs, "no profile") {
		t.Errorf("recreate without a profile = (%d, %q); want a no-profile error", code, errs)
	}
}

// TestRecreateInContainerBlocked: recreate is a mutating command — blocked
// inside the container like create/rm.
func TestRecreateInContainerBlocked(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "1")
	code, _, errs := run("recreate", "x", "--src", t.TempDir())
	if code != 1 || !strings.Contains(errs, "not available inside the sandbox") {
		t.Errorf("recreate in container = (%d, %q)", code, errs)
	}
}
