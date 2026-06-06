//go:build integration

package backend

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/itest"
)

// realRunOpts builds a minimal RunOpts for a real engine run: egress disabled,
// a credential-less HOME (so authFlags binds nothing), and a throwaway sandbox
// dir mounted rw and used as the workdir.
func realRunOpts(t *testing.T, engine, image, dest string, args ...string) RunOpts {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	return RunOpts{
		Engine: engine, Image: image, Dest: dest, Slug: "itest",
		RT:       config.Runtime{}, // Egress=false ⇒ no allowlist required
		NoEgress: true,
		Args:     args,
	}
}

// TestRun_RealEngine_NoEgress_ExitAndMount runs a real container and proves the
// rw bind mount works end-to-end: the file the container writes under
// $SANDBOXER_SANDBOX_DIR appears on the host under Dest.
func TestRun_RealEngine_NoEgress_ExitAndMount(t *testing.T) {
	engine := itest.Engine(t)
	image := itest.SmokeImage(t, engine)
	dest := t.TempDir()

	o := realRunOpts(t, engine, image, dest,
		"sh", "-c", `printf hi > "$SANDBOXER_SANDBOX_DIR/marker"; echo ran`)
	var out bytes.Buffer
	o.Stdout, o.Stderr = &out, &out
	code, err := Run(o)
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "ran") {
		t.Errorf("stdout = %q, want it to contain 'ran'", out.String())
	}
	if b, err := os.ReadFile(filepath.Join(dest, "marker")); err != nil || string(b) != "hi" {
		t.Errorf("marker on host = %q (err %v) — rw bind mount did not propagate", b, err)
	}
}

// TestRun_RealEngine_ExitCodePropagation proves the child's real exit code flows
// back through exitCode(cmd.Run()).
func TestRun_RealEngine_ExitCodePropagation(t *testing.T) {
	engine := itest.Engine(t)
	image := itest.SmokeImage(t, engine)

	o := realRunOpts(t, engine, image, t.TempDir(), "sh", "-c", "exit 7")
	var buf bytes.Buffer
	o.Stdout, o.Stderr = &buf, &buf
	code, err := Run(o)
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, buf.String())
	}
	if code != 7 {
		t.Errorf("exit = %d, want 7 (real child code)", code)
	}
}

// TestRunArgv_RealEngineAccepts feeds the argv RunArgv generates straight to the
// engine. The unit TestRunArgv only checks the string; this proves the engine
// actually parses every flag we emit (no "unknown flag" regressions).
func TestRunArgv_RealEngineAccepts(t *testing.T) {
	engine := itest.Engine(t)
	image := itest.SmokeImage(t, engine)
	t.Setenv("HOME", t.TempDir())

	argv, err := RunArgv(RunOpts{
		Engine: engine, Image: image, Dest: t.TempDir(), Slug: "itest",
		RT: config.Runtime{}, NoEgress: true, Args: []string{"true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	cmd := exec.Command(engine, argv...)
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		t.Fatalf("engine rejected generated argv: %v\n%s\nargv: %v", err, buf.String(), argv)
	}
}

// TestRun_RealEngine_WallTimeoutKills checks the in-container `timeout` wrapper
// terminates a hung command. It needs GNU coreutils `timeout` (the toolbox
// image); busybox's differs in flags, so this gates on the toolbox image.
func TestRun_RealEngine_WallTimeoutKills(t *testing.T) {
	engine := itest.Engine(t)
	image := itest.EnsureToolboxImage(t, engine)

	o := realRunOpts(t, engine, image, t.TempDir(), "sleep", "30")
	o.Wall = "1"
	var buf bytes.Buffer
	o.Stdout, o.Stderr = &buf, &buf
	code, err := Run(o)
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, buf.String())
	}
	if code == 0 {
		t.Errorf("exit = 0, want non-zero (timeout should have killed sleep)")
	}
}
