//go:build integration

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/backend"
	"github.com/irasikhin/sandboxer/internal/itest"
)

// engineQuery runs a read-only engine query and returns its trimmed stdout,
// failing the test on error.
func engineQuery(t *testing.T, engine string, args ...string) string {
	t.Helper()
	out, err := exec.Command(engine, args...).Output()
	if err != nil {
		t.Fatalf("%s %s: %v", engine, strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

// TestSessionLifecycle_Container_EnterStopRm drives the persistent-session
// lifecycle through the real CLI against a real engine WITH the egress
// allowlist on: enter creates the session + its stably-named egress sidecar,
// exec rides the running session (tmux probed detached — never an interactive
// TTY), stop parks the container and proxy but keeps the networks, re-enter
// resumes the same container with its state, and rm sweeps every engine
// resource (container, proxy, networks). Needs the toolbox image: the egress
// proxy runs the baked sandboxer binary and the probes need the baked tmux.
func TestSessionLifecycle_Container_EnterStopRm(t *testing.T) {
	engine := itest.Engine(t)
	image := itest.EnsureToolboxImage(t, engine)
	t.Setenv("SANDBOXER_ENGINE", engine)
	t.Setenv("SANDBOXER_IMAGE", image)
	t.Setenv("SANDBOXER_NO_EGRESS", "")
	t.Setenv("SANDBOXER_SESSION", "")
	t.Setenv("SANDBOXER_NO_AUTOBUILD", "1") // a missing image must skip above, never build here

	project := newProject(t)
	t.Setenv("HOME", t.TempDir()) // no host creds bound into the container
	cfg := filepath.Join(t.TempDir(), "sbx.yaml")
	body := "name: feat\nbackend: " + engine + "\nagent: claude\n" +
		"network:\n  allowedDomains: [example.com]\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	if code, out, errs := run("create", "--src", project, "--config", cfg); code != 0 {
		t.Fatalf("create = %d\nout: %s\nerr: %s", code, out, errs)
	}
	name := backend.SessionName("feat", filepath.Join(project, ".sandboxer"))
	// Cleanups run LIFO: register networks first so the containers holding them
	// are removed first.
	itest.CleanupNetwork(t, engine, name+"-int")
	itest.CleanupNetwork(t, engine, name+"-ext")
	itest.CleanupContainer(t, engine, name+"-proxy")
	itest.CleanupContainer(t, engine, name)

	// 1. enter: converges the session container + egress sidecar. The tmux
	// attach itself fails (the test has no TTY) so the exit code is non-zero —
	// the converged engine state below is what this step asserts.
	_, _, errs := run("enter", "feat", "--src", project, "--config", cfg)
	if info := backend.InspectSession(engine, name); !info.Exists || !info.Running {
		t.Fatalf("after enter: session info = %+v, want exists+running\nstderr: %s", info, errs)
	}
	if got := engineQuery(t, engine, "container", "inspect", "--format", "{{.State.Running}}", name+"-proxy"); got != "true" {
		t.Fatalf("egress proxy running = %q, want true", got)
	}

	// 2. tmux probes via exec — detached new-session, then list-sessions.
	if code, _, errs := run("exec", "feat", "--src", project, "--config", cfg, "--",
		"tmux", "-L", "sandboxer", "new-session", "-d", "-s", "probe"); code != 0 {
		t.Fatalf("tmux new-session = %d\n%s", code, errs)
	}
	if code, out, errs := run("exec", "feat", "--src", project, "--config", cfg, "--",
		"tmux", "-L", "sandboxer", "list-sessions"); code != 0 || !strings.Contains(out, "probe") {
		t.Fatalf("tmux list-sessions = (%d, %q)\n%s", code, out, errs)
	}
	// A marker in the container's own fs proves exec rode the session (a
	// one-shot fallback would get a fresh /tmp) and must survive stop/start.
	if code, _, errs := run("exec", "feat", "--src", project, "--config", cfg, "--",
		"sh", "-c", "echo alive > /tmp/probe"); code != 0 {
		t.Fatalf("marker exec = %d\n%s", code, errs)
	}

	// 3. stop: container and proxy parked, networks kept for the resume.
	if code, out, errs := run("stop", "feat", "--src", project, "--config", cfg); code != 0 ||
		!strings.Contains(out, "stopped session") {
		t.Fatalf("stop = (%d, %q)\n%s", code, out, errs)
	}
	if info := backend.InspectSession(engine, name); !info.Exists || info.Running {
		t.Fatalf("after stop: session info = %+v, want exists+stopped", info)
	}
	if got := engineQuery(t, engine, "container", "inspect", "--format", "{{.State.Running}}", name+"-proxy"); got != "false" {
		t.Errorf("egress proxy running after stop = %q, want false", got)
	}
	if nets := engineQuery(t, engine, "network", "ls", "--filter", "name="+name, "--format", "{{.Name}}"); nets == "" {
		t.Error("egress networks gone after stop — stop must keep them for the resume")
	}

	// 4. re-enter: resumes the SAME container (plain start + proxy revive); the
	// marker written before the stop is still there.
	_, _, errs = run("enter", "feat", "--src", project, "--config", cfg)
	if info := backend.InspectSession(engine, name); !info.Exists || !info.Running {
		t.Fatalf("after re-enter: session info = %+v, want exists+running\nstderr: %s", info, errs)
	}
	if got := engineQuery(t, engine, "container", "inspect", "--format", "{{.State.Running}}", name+"-proxy"); got != "true" {
		t.Errorf("egress proxy running after re-enter = %q, want true", got)
	}
	if code, out, errs := run("exec", "feat", "--src", project, "--config", cfg, "--",
		"cat", "/tmp/probe"); code != 0 || !strings.Contains(out, "alive") {
		t.Errorf("marker after stop/re-enter = (%d, %q) — session state must survive a stop\n%s", code, out, errs)
	}

	// 5. rm: the sandbox AND every engine resource go — container, proxy,
	// egress networks.
	if code, _, errs := run("rm", "feat", "--src", project, "--config", cfg); code != 0 {
		t.Fatalf("rm = %d\n%s", code, errs)
	}
	if ps := engineQuery(t, engine, "ps", "-a", "--filter", "name="+name, "--format", "{{.Names}}"); ps != "" {
		t.Errorf("containers left after rm: %q", ps)
	}
	if nets := engineQuery(t, engine, "network", "ls", "--filter", "name="+name, "--format", "{{.Name}}"); nets != "" {
		t.Errorf("networks left after rm: %q", nets)
	}
}
