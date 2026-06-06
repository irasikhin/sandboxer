//go:build integration

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/itest"
)

// writeDepFixture creates an origin tree (<root>/mylib/d.txt = "v1\n") and
// returns the root and the origin file path. A profile pointing roots→root and
// deps→["mylib"] vendors it into the sandbox.
func writeDepFixture(t *testing.T) (root, originFile string) {
	t.Helper()
	root = t.TempDir()
	lib := filepath.Join(root, "mylib")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	originFile = filepath.Join(lib, "d.txt")
	if err := os.WriteFile(originFile, []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, originFile
}

func writeLifecycleProfile(t *testing.T, backend, root string) string {
	t.Helper()
	body := "name: feat\nbackend: " + backend + "\nagent: claude\negress: false\n" +
		"roots:\n  - " + root + "\ndeps:\n  - mylib\n"
	p := filepath.Join(t.TempDir(), "sbx.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestLifecycle_Native_CreateDiffPushExec drives the full lifecycle on the
// native backend (no engine needed): create vendors the dep, an edit shows up in
// a real diff(1), push returns it to the origin, and exec runs a command in the
// sandbox and auto-pushes the change back. Exercises srcs.CopyIn/CopyOut, the
// diff command and NativeExec for real.
func TestLifecycle_Native_CreateDiffPushExec(t *testing.T) {
	requireExec(t, "sh", "diff")
	project := newProject(t)
	root, originFile := writeDepFixture(t)
	cfg := writeLifecycleProfile(t, "native", root)

	// 1. create → dep vendored into the sandbox copy; manifest written.
	if code, out, errs := run("create", "--src", project, "--config", cfg); code != 0 {
		t.Fatalf("create = %d\nout: %s\nerr: %s", code, out, errs)
	}
	sandboxFile := filepath.Join(project, ".sandboxer", "feat", "mylib", "d.txt")
	if b, err := os.ReadFile(sandboxFile); err != nil || string(b) != "v1\n" {
		t.Fatalf("dep not vendored: got %q err %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(project, ".sandboxer", "_meta", "feat.manifest.json")); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}

	// 2. edit the sandbox copy; a real diff(1) must show it.
	if err := os.WriteFile(sandboxFile, []byte("DIFFED\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := run("diff", "feat", "--src", project); code != 0 || !strings.Contains(out, "DIFFED") {
		t.Fatalf("diff = (%d, %q), want it to contain DIFFED", code, out)
	}

	// 3. push → origin overwritten with the sandbox copy.
	if code, _, errs := run("push", "feat", "--src", project); code != 0 {
		t.Fatalf("push = %d\n%s", code, errs)
	}
	if b, _ := os.ReadFile(originFile); string(b) != "DIFFED\n" {
		t.Fatalf("origin after push = %q, want DIFFED", b)
	}

	// 4. exec runs in the sandbox and auto-pushes back to the origin.
	if code, _, errs := run("exec", "feat", "--src", project, "--", "sh", "-c", "printf EXECED > mylib/d.txt"); code != 0 {
		t.Fatalf("exec = %d\n%s", code, errs)
	}
	if b, _ := os.ReadFile(originFile); string(b) != "EXECED" {
		t.Fatalf("origin after exec auto-push = %q, want EXECED", b)
	}
}

// TestLifecycle_Container_ExecPush drives create + exec through a REAL container
// engine (egress disabled, smoke image). The in-container edit lands on the host
// sandbox copy via the rw bind mount, and exec's auto-push returns it to the
// origin — proving the container backend's mount + push round-trip.
func TestLifecycle_Container_ExecPush(t *testing.T) {
	requireExec(t, "sh")
	engine := itest.Engine(t)
	image := itest.SmokeImage(t, engine)
	t.Setenv("SANDBOXER_ENGINE", engine)
	t.Setenv("SANDBOXER_IMAGE", image)
	t.Setenv("SANDBOXER_NO_EGRESS", "1")

	project := newProject(t)
	t.Setenv("HOME", t.TempDir()) // no creds to bind into the container
	root, originFile := writeDepFixture(t)
	cfg := writeLifecycleProfile(t, engine, root)

	if code, out, errs := run("create", "--src", project, "--config", cfg); code != 0 {
		t.Fatalf("create = %d\nout: %s\nerr: %s", code, out, errs)
	}
	if code, out, errs := run("exec", "feat", "--src", project, "--", "sh", "-c", "printf C0NTAINED > mylib/d.txt"); code != 0 {
		t.Fatalf("exec = %d\nout: %s\nerr: %s", code, out, errs)
	}
	if b, _ := os.ReadFile(originFile); string(b) != "C0NTAINED" {
		t.Fatalf("origin after container exec auto-push = %q, want C0NTAINED", b)
	}
}
