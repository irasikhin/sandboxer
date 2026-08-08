package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
)

// stubSessionOrphans replaces doctor's orphan-enumeration seam.
func stubSessionOrphans(t *testing.T, orphans []string, err error) {
	t.Helper()
	old := sessionOrphans
	t.Cleanup(func() { sessionOrphans = old })
	sessionOrphans = func(engine string) ([]string, error) { return orphans, err }
}

// doctorEnv gives doctor an engine and an isolated cwd. The
// engine enumeration is pinned to podman so a real docker on the host cannot
// add extra session rows.
func doctorEnv(t *testing.T) {
	t.Helper()
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	fakePodman(t)
	stubInstalledEngines(t, []string{"podman"})
	t.Chdir(t.TempDir())
}

// TestDoctorMicrosandboxRow: doctor reports the second microVM runner beside
// smolvm — absent is a warning with an install hint (nobody needs it unless
// they picked that backend), and a present msb whose MSB_HOME is too deep is
// flagged BEFORE the first create fails on an over-long agent socket.
func TestDoctorMicrosandboxRow(t *testing.T) {
	doctorEnv(t)
	stubSessionStates(t, map[string]string{}, nil)
	stubSessionOrphans(t, nil, nil)

	t.Setenv("SANDBOXER_MSB", "/nonexistent/msb-xyz")
	_, out, _ := run("doctor")
	if !strings.Contains(out, "microsandbox (msb)") || !strings.Contains(out, "SANDBOXER_MSB") {
		t.Errorf("doctor missing the microsandbox row:\n%s", out)
	}

	dir := t.TempDir()
	bin := filepath.Join(dir, "msb")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'msb 0.6.7-fake'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SANDBOXER_MSB", bin)
	t.Setenv("MSB_HOME", filepath.Join("/home/dev", strings.Repeat("deep/", 20), ".msb"))
	_, out, _ = run("doctor")
	if !strings.Contains(out, "MSB_HOME is too deep") {
		t.Errorf("doctor did not flag a too-deep MSB_HOME:\n%s", out)
	}
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

// TestDoctorOrphanProbeFailureIsSilent: the orphan scan is purely advisory —
// its failure adds no row and no warning.
func TestDoctorOrphanProbeFailureIsSilent(t *testing.T) {
	doctorEnv(t)
	stubSessionStates(t, map[string]string{}, nil)
	stubSessionOrphans(t, nil, errors.New("probe failed"))

	code, out, _ := run("doctor")
	if code != 0 {
		t.Fatalf("doctor = %d", code)
	}
	if strings.Contains(out, "orphan") || strings.Contains(out, "probe failed") {
		t.Errorf("a failing orphan probe must stay silent:\n%s", out)
	}
}

// TestDoctorSessionsEveryEngine: with both engines installed doctor probes
// each one — a podman-backed session must not be invisible just because
// docker is the auto-detected default.
func TestDoctorSessionsEveryEngine(t *testing.T) {
	doctorEnv(t)
	stubInstalledEngines(t, []string{"podman", "docker"})
	stubSessionStates(t, map[string]string{"a": "running"}, nil)
	stubSessionOrphans(t, nil, nil)

	code, out, _ := run("doctor")
	if code != 0 {
		t.Fatalf("doctor = %d", code)
	}
	for _, want := range []string{"sessions (podman)", "sessions (docker)"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor missing the %q row:\n%s", want, out)
		}
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

// stubGitCheckIgnore pins the advisory gitignore probe.
func stubGitCheckIgnore(t *testing.T, ignored bool) {
	t.Helper()
	old := gitCheckIgnore
	t.Cleanup(func() { gitCheckIgnore = old })
	gitCheckIgnore = func(root, rel string) bool { return ignored }
}

// TestDoctorWarnsIgnoredConfig: when the repo's gitignore hides
// sandboxer.nix, doctor adds a warning row; when it doesn't, no row.
func TestDoctorWarnsIgnoredConfig(t *testing.T) {
	project := newProject(t)
	t.Chdir(project)
	stubInstalledEngines(t, nil)
	if err := os.WriteFile(config.ConfigPath(), []byte("{ name = \"feat\"; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stubGitCheckIgnore(t, true)
	if code, out, _ := run("doctor"); code != 0 || !strings.Contains(out, "ignored by the repo's gitignore") {
		t.Errorf("doctor with ignored config = (%d, %q); want the gitignore warning", code, out)
	}

	stubGitCheckIgnore(t, false)
	if code, out, _ := run("doctor"); code != 0 || strings.Contains(out, "ignored by the repo's gitignore") {
		t.Errorf("doctor without ignored config = (%d, %q); want no gitignore warning", code, out)
	}
}

// TestCreateWarnsIgnoredConfig: create prints the same advisory on stderr,
// best-effort — it never fails the command.
func TestCreateWarnsIgnoredConfig(t *testing.T) {
	project := newProject(t)
	stubGitCheckIgnore(t, true)
	code, _, errs := run("create", "feat", "--src", project)
	if code != 0 {
		t.Fatalf("create = %d, %s", code, errs)
	}
	if !strings.Contains(errs, "ignored by your repo's gitignore") {
		t.Errorf("create missing the gitignore advisory: %q", errs)
	}
}

// TestGitCheckIgnoreReal exercises the real `git check-ignore` mapping: a
// repo whose root gitignore lists sandboxer.nix ignores the config; a plain
// directory (no repo) reads as not-ignored.
func TestGitCheckIgnoreReal(t *testing.T) {
	requireExec(t, "git")
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git unusable here: %v (%s)", err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(config.ConfigFileName+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !gitCheckIgnore(dir, config.ConfigPath()) {
		t.Error("an ignore rule for the config must read as ignored")
	}
	if gitCheckIgnore(t.TempDir(), config.ConfigPath()) {
		t.Error("a non-repo dir must read as not-ignored")
	}
}

// stubHostSubIDs replaces doctor's subordinate-range lookup seam (the real one
// reads the host's /etc/subuid, which a test cannot control).
func stubHostSubIDs(t *testing.T, uids, gids int) {
	t.Helper()
	old := hostSubIDCounts
	t.Cleanup(func() { hostSubIDCounts = old })
	hostSubIDCounts = func() (int, int) { return uids, gids }
}

// writeNestedConfig drops a minimal nestedContainers profile in the cwd.
func writeNestedConfig(t *testing.T, backendName string) {
	t.Helper()
	cfg := "{ name = \"n\"; backend = \"" + backendName + "\"; nestedContainers = true;\n" +
		"  srcs = [ { src = \".\"; branch = \"feat/n\"; } ]; }\n"
	if err := os.WriteFile(config.ConfigFileName, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDoctorNestedContainers: a profile that opted in gets a verdict on
// whether MULTI-uid nested containers can actually work here — the difference
// between `podman run postgres` running and dying with EINVAL. The podman
// engine needs host subordinate ranges (warn when missing); a docker engine is
// steered at the backend that does support it.
func TestDoctorNestedContainers(t *testing.T) {
	doctorEnv(t)
	stubSessionStates(t, map[string]string{}, nil)
	stubSessionOrphans(t, nil, nil)

	// podman + host ranges: the working combination.
	writeNestedConfig(t, "podman")
	stubHostSubIDs(t, 65536, 65536)
	_, out, _ := run("doctor")
	if !strings.Contains(out, "nestedContainers (n)") || !strings.Contains(out, "multi-uid nested containers work") {
		t.Errorf("doctor missing the nested ok row:\n%s", out)
	}

	// podman without ranges: the actionable warning, and --strict must fail on it.
	stubHostSubIDs(t, 0, 0)
	code, out, _ := run("doctor", "--strict")
	if !strings.Contains(out, "no subordinate uid/gid ranges") || !strings.Contains(out, "usermod") {
		t.Errorf("doctor missing the no-ranges warning:\n%s", out)
	}
	if code == 0 {
		t.Error("--strict must fail while multi-uid cannot work")
	}

	// docker: single-uid is a hard limit there, so the row steers at podman.
	writeNestedConfig(t, "docker")
	stubHostSubIDs(t, 65536, 65536)
	_, out, _ = run("doctor")
	if !strings.Contains(out, "single-uid on a docker engine") {
		t.Errorf("doctor did not steer the docker user at podman:\n%s", out)
	}

	// A microVM backend warn-and-ignores the knob; doctor says so instead of
	// judging host subuids that play no part there.
	writeNestedConfig(t, "microvm")
	_, out, _ = run("doctor")
	if !strings.Contains(out, "ignored on microvm") {
		t.Errorf("doctor did not report the microvm ignore:\n%s", out)
	}

	// A profile that did not opt in gets no row at all.
	if err := os.WriteFile(config.ConfigFileName,
		[]byte("{ name = \"n\"; srcs = [ { src = \".\"; branch = \"feat/n\"; } ]; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, out, _ = run("doctor")
	if strings.Contains(out, "nestedContainers") {
		t.Errorf("doctor reported nestedContainers for a profile that did not ask:\n%s", out)
	}
}

// TestScaffoldMentionsNestedContainers: the knob is opt-in, so the scaffolded
// config is the one place a user meets it — commented out, with its cost named
// and the podman prerequisite for user-switching images. It must also stay
// commented: uncommenting is the user's call, and the file has to keep
// evaluating (the whole scaffold is parsed by config init's own tests).
func TestScaffoldMentionsNestedContainers(t *testing.T) {
	s := starterProfile("demo", config.LoadDefaults())
	for _, want := range []string{
		"# nestedContainers = true;",
		"docker run postgres",
		"SECURITY.md",
		`backend = "podman"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("scaffold missing %q", want)
		}
	}
	if strings.Contains(s, "\n  nestedContainers = true;") {
		t.Error("the scaffold must leave nestedContainers commented out — opting in is the user's call")
	}
}
