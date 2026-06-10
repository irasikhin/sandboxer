//go:build integration

package backend

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/itest"
)

// engineOut runs an engine query and returns its trimmed stdout, failing the
// test on error (these are read-only inspect/ps/network calls).
func engineOut(t *testing.T, engine string, args ...string) string {
	t.Helper()
	out, err := exec.Command(engine, args...).Output()
	if err != nil {
		t.Fatalf("%s %s: %v", engine, strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

// execSessionOut runs cmdArgs in the session container via ExecSession and
// returns (exit code, combined output).
func execSessionOut(t *testing.T, o RunOpts, name string, cmdArgs ...string) (int, string) {
	t.Helper()
	var buf bytes.Buffer
	o.Stdout, o.Stderr = &buf, &buf
	code, err := ExecSession(o, name, cmdArgs)
	if err != nil {
		t.Fatalf("ExecSession %v: %v\n%s", cmdArgs, err, buf.String())
	}
	return code, buf.String()
}

// TestSession_RealEngine_Lifecycle drives the whole persistent-session state
// machine against a real engine (egress disabled, smoke image): create stamps
// the discovery labels, state persists across execs, a config change recreates
// the idle session, and rm leaves no container or network behind.
func TestSession_RealEngine_Lifecycle(t *testing.T) {
	engine := itest.Engine(t)
	image := itest.SmokeImage(t, engine)

	slug := itest.Slug("sess")
	baseDir := t.TempDir()
	var notices bytes.Buffer
	o := RunOpts{
		Engine: engine, Image: image, Dest: t.TempDir(), Slug: slug,
		BaseDir: baseDir, HomeDir: t.TempDir(),
		RT: config.Runtime{}, NoEgress: true,
		Stderr: &notices,
	}
	name := SessionName(slug, baseDir)
	itest.CleanupContainer(t, engine, name)

	// 1. Create: EnsureSession converges from nothing to a running container.
	got, err := EnsureSession(o)
	if err != nil {
		t.Fatalf("EnsureSession: %v\n%s", err, notices.String())
	}
	if got != name {
		t.Fatalf("session name = %q, want %q", got, name)
	}
	info := InspectSession(engine, name)
	if !info.Exists || !info.Running {
		t.Fatalf("after create: info = %+v, want exists+running", info)
	}
	if want := SessionWantHash(o); info.Hash != want {
		t.Errorf("recorded hash = %q, want %q (stamped hash must match the oracle)", info.Hash, want)
	}

	// 2. Labels: the engine's container store is the only session state, so the
	// discovery labels must round-trip through a real inspect.
	labels := engineOut(t, engine, "container", "inspect", "--format",
		`{{index .Config.Labels "`+LabelManaged+`"}} {{index .Config.Labels "`+LabelSlug+`"}} {{index .Config.Labels "`+LabelBase+`"}}`,
		name)
	if want := "true " + slug + " " + baseDir; labels != want {
		t.Errorf("labels = %q, want %q", labels, want)
	}

	// 3. Persistence: a file written by one exec is seen by the next (one-shot
	// runs get a fresh /tmp every time; the session must not).
	if code, out := execSessionOut(t, o, name, "sh", "-c", "echo persisted > /tmp/state"); code != 0 {
		t.Fatalf("write exec = %d\n%s", code, out)
	}
	if code, out := execSessionOut(t, o, name, "cat", "/tmp/state"); code != 0 || !strings.Contains(out, "persisted") {
		t.Fatalf("read exec = (%d, %q), want the state written by the previous exec", code, out)
	}
	id1 := engineOut(t, engine, "container", "inspect", "--format", "{{.Id}}", name)

	// 4. Stale config: a changed create argv flips the hash; with no tmux client
	// attached (the smoke image has no tmux — that counts as idle) the session
	// is recreated under the same name.
	o2 := o
	o2.RT.NoProxy = "stale.invalid"
	if _, err := EnsureSession(o2); err != nil {
		t.Fatalf("EnsureSession (stale): %v\n%s", err, notices.String())
	}
	if !strings.Contains(notices.String(), "recreating session") {
		t.Errorf("missing recreate notice:\n%s", notices.String())
	}
	id2 := engineOut(t, engine, "container", "inspect", "--format", "{{.Id}}", name)
	if id1 == id2 {
		t.Error("stale session was not replaced (same container id)")
	}
	if info := InspectSession(engine, name); info.Hash != SessionWantHash(o2) {
		t.Errorf("recreated hash = %q, want %q", info.Hash, SessionWantHash(o2))
	}
	if code, _ := execSessionOut(t, o2, name, "cat", "/tmp/state"); code == 0 {
		t.Error("recreated session still has the old state — it was not actually replaced")
	}

	// 5. Remove: no container and no session-named network may survive.
	if err := RemoveSession(engine, slug, baseDir); err != nil {
		t.Fatalf("RemoveSession: %v", err)
	}
	if ps := engineOut(t, engine, "ps", "-a", "--filter", "label="+LabelSlug+"="+slug,
		"--format", "{{.Names}}"); ps != "" {
		t.Errorf("containers left after rm: %q", ps)
	}
	if nets := engineOut(t, engine, "network", "ls", "--filter", "name="+name,
		"--format", "{{.Name}}"); nets != "" {
		t.Errorf("networks left after rm: %q", nets)
	}
}

// TestSession_RealEngine_StopStart proves StopSession keeps the container (and
// its filesystem) so a later EnsureSession resumes it with a plain start —
// same container id, state intact.
func TestSession_RealEngine_StopStart(t *testing.T) {
	engine := itest.Engine(t)
	image := itest.SmokeImage(t, engine)

	slug := itest.Slug("sess")
	baseDir := t.TempDir()
	o := RunOpts{
		Engine: engine, Image: image, Dest: t.TempDir(), Slug: slug,
		BaseDir: baseDir, HomeDir: t.TempDir(),
		RT: config.Runtime{}, NoEgress: true,
	}
	name := SessionName(slug, baseDir)
	itest.CleanupContainer(t, engine, name)

	if _, err := EnsureSession(o); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	if code, out := execSessionOut(t, o, name, "sh", "-c", "echo kept > /tmp/state"); code != 0 {
		t.Fatalf("write exec = %d\n%s", code, out)
	}
	id1 := engineOut(t, engine, "container", "inspect", "--format", "{{.Id}}", name)

	if err := StopSession(engine, slug, baseDir); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	if info := InspectSession(engine, name); !info.Exists || info.Running {
		t.Fatalf("after stop: info = %+v, want exists+stopped", info)
	}

	if _, err := EnsureSession(o); err != nil {
		t.Fatalf("EnsureSession (restart): %v", err)
	}
	if id2 := engineOut(t, engine, "container", "inspect", "--format", "{{.Id}}", name); id2 != id1 {
		t.Error("restart replaced the container — a fresh-config stop must resume, not recreate")
	}
	if code, out := execSessionOut(t, o, name, "cat", "/tmp/state"); code != 0 || !strings.Contains(out, "kept") {
		t.Errorf("state after stop/start = (%d, %q), want it kept", code, out)
	}
	if err := RemoveSession(engine, slug, baseDir); err != nil {
		t.Errorf("RemoveSession: %v", err)
	}
}
