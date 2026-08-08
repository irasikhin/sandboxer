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
	"github.com/irasikhin/sandboxer/internal/sandbox"
	"github.com/irasikhin/sandboxer/internal/seccomp"
)

// realRunOpts builds a minimal RunOpts for a real engine run: egress disabled,
// an isolated throwaway agent home mounted as $HOME, and a throwaway sandbox
// dir mounted rw and used as the workdir. No host credentials are involved.
func realRunOpts(t *testing.T, engine, image, dest string, args ...string) RunOpts {
	t.Helper()
	return RunOpts{
		MountDest: true, Engine: engine, Image: image, Dest: dest, Slug: "itest",
		HomeDir:  t.TempDir(),
		RT:       config.Runtime{}, // Egress=false ⇒ no allowlist required
		NoEgress: true,
		Args:     args,
	}
}

// nestedHome is the agent home for a nested-containers run. It cannot be a
// plain t.TempDir: the nested podman's image store lands under it owned by
// MAPPED uids, which the invoking user cannot unlink — the cleanup fails with
// EPERM and takes the test with it. So the removal is delegated to a container
// whose root maps over exactly those ids.
func nestedHome(t *testing.T, engine, image string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sbx-nested-home-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = exec.Command(engine, "run", "--rm", "--volume", dir+":/h", image,
			"rm", "-rf", "/h/.local", "/h/.config").Run()
		_ = os.RemoveAll(dir)
	})
	return dir
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

// TestRun_RealEngine_NestedMultiUID proves the whole multi-uid chain on a real
// podman engine: ambient SETUID/SETGID + the generated /etc/{passwd,group,
// subuid,subgid} let the NESTED podman run an image under a user other than
// the sandbox uid — the exact thing a single-uid mapping cannot do (postgres
// et al. setresuid and die with EINVAL). Skips on docker (no ambient caps for
// a non-root user — the grant is deliberately podman-only), on a host user
// without subordinate ranges, and without the toolbox image (it carries the
// nested podman + newuidmap).
func TestRun_RealEngine_NestedMultiUID(t *testing.T) {
	engine := itest.Engine(t)
	if engine != "podman" {
		t.Skipf("multi-uid nested podman needs a podman outer engine (got %s)", engine)
	}
	image := itest.EnsureToolboxImage(t, engine)
	itest.RequireLiveEgress(t) // the nested pull reaches a real registry

	t.Setenv("XDG_STATE_HOME", t.TempDir())
	base, err := sandbox.ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ok, err := base.WriteNestedIDFiles("itest")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Skip("host user has no subordinate uid/gid ranges — multi-uid impossible here")
	}

	dest := t.TempDir()
	o := realRunOpts(t, engine, image, dest,
		"bash", "-lc", "podman run --rm --user 999:999 docker.io/library/alpine id -u")
	o.HomeDir = nestedHome(t, engine, image)
	o.Profile = &config.Profile{NestedContainers: true}
	o.NestedIDFiles = NestedIDFiles(base.NestedIDFiles("itest"))
	// The purpose-built profile, exactly as enter passes it — this test is the
	// completeness oracle for internal/seccomp's syscall list AND for the
	// podman-scoped unmask=/proc/* (a miss surfaces as "cannot clone" /
	// "mount `proc`" EPERM here, never in unit tests).
	scPath, err := seccomp.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	o.NestedSeccompPath = scPath
	var out bytes.Buffer
	o.Stdout, o.Stderr = &out, &out
	code, err := Run(o)
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	if code != 0 {
		t.Fatalf("exit = %d, want 0 — nested user-switching run failed:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "999") {
		t.Errorf("nested id -u = %q, want 999", out.String())
	}
}

// TestRun_RealEngine_NestedSingleUID proves the single-uid nested path — the
// one docker engines (and range-less podman hosts) live on — under the
// purpose-built seccomp profile. Runs on BOTH engines: no id files, so no
// multi-uid grant; the nested podman maps one uid and an image that does not
// switch user must still run, pull included.
func TestRun_RealEngine_NestedSingleUID(t *testing.T) {
	engine := itest.Engine(t)
	image := itest.EnsureToolboxImage(t, engine)
	itest.RequireLiveEgress(t) // the nested pull reaches a real registry

	dest := t.TempDir()
	// The marker matters: the engine's own progress chatter is full of bare
	// digits, so asserting on "0" alone would pass even when nothing ran.
	o := realRunOpts(t, engine, image, dest,
		"bash", "-lc", `podman run --rm docker.io/library/alpine sh -c 'echo nested-uid=$(id -u)'`)
	o.HomeDir = nestedHome(t, engine, image)
	o.Profile = &config.Profile{NestedContainers: true}
	scPath, err := seccomp.Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	o.NestedSeccompPath = scPath
	var out bytes.Buffer
	o.Stdout, o.Stderr = &out, &out
	code, err := Run(o)
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	if code != 0 {
		t.Fatalf("exit = %d, want 0 — nested single-uid run failed under the seccomp profile:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "nested-uid=0") {
		t.Errorf("nested id -u = %q, want 0 (nested root)", out.String())
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
		MountDest: true, Engine: engine, Image: image, Dest: t.TempDir(), Slug: "itest",
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
