package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeMsb points SANDBOXER_MSB at a no-op msb stand-in (list answers an empty
// inventory, everything else exits 0), isolates HOME and the state root, and
// selects a custom public image so enter/exec never trigger the host-nix
// auto-build. SANDBOXER_BACKEND selects the microsandbox backend so a bare
// command needs no --backend flag.
func fakeMsb(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "msb")
	script := "#!/bin/sh\nif [ \"$1\" = list ]; then echo '[]'; fi\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SANDBOXER_MSB", bin)
	t.Setenv("SANDBOXER_BACKEND", "microsandbox")
	t.Setenv("SANDBOXER_STATE", filepath.Join(dir, "state"))
	t.Setenv("SANDBOXER_IMAGE", "example.com/toolbox:test")
	t.Setenv("HOME", t.TempDir())
}

// varTmpProject is newProject relocated under /var/tmp, for the tests that
// drive the REAL backend lifecycle end to end: msbPreflight refuses any share
// under /tmp (the guest mounts a tmpfs over it), and t.TempDir lives exactly
// there — so the project (whose ./sandboxes/<slug> becomes the sandbox root
// share) must sit elsewhere. Mirrors internal/itest.MSBTempDir; skips when
// /var/tmp is not writable.
func varTmpProject(t *testing.T) string {
	t.Helper()
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	root, err := os.MkdirTemp("/var/tmp", "sandboxer-cli-msb-")
	if err != nil {
		t.Skipf("no writable /var/tmp for a non-/tmp share: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "f.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitInitProject(t, project)
	return project
}

// fakeMsbE2E is fakeMsb with the state root relocated beside the /var/tmp
// project, so the sandbox HOME share (under the state dir) passes msbPreflight
// exactly like the sandbox root does.
func fakeMsbE2E(t *testing.T, project string) {
	t.Helper()
	fakeMsb(t)
	t.Setenv("SANDBOXER_STATE", filepath.Join(filepath.Dir(project), "state"))
}

// TestRunEnterExecMsb drives create → enter → exec through the REAL backend
// lifecycle (EnsureSession/ExecSession, the record store, the msb argv
// construction — including msbPreflight on the real share paths) against the
// no-op msb stand-in.
func TestRunEnterExecMsb(t *testing.T) {
	requireExec(t, "sh")
	project := varTmpProject(t)
	fakeMsbE2E(t, project)

	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}
	if code, _, errs := run("enter", "feat", "--src", project, "--backend", "microsandbox"); code != 0 {
		t.Errorf("enter machine = %d, %s", code, errs)
	}
	if code, _, errs := run("exec", "feat", "--src", project, "--backend", "microsandbox", "--", "echo", "hi"); code != 0 {
		t.Errorf("exec machine = %d, %s", code, errs)
	}
}

func TestRunEnterAutoCreate(t *testing.T) {
	requireExec(t, "sh")
	project := varTmpProject(t)
	fakeMsbE2E(t, project)
	if code, _, errs := run("enter", "fresh", "--src", project); code != 0 {
		t.Fatalf("enter auto-create = %d, %s", code, errs)
	}
	if _, err := os.Stat(sandboxDir(project, "fresh")); err != nil {
		t.Errorf("enter did not create the sandbox: %v", err)
	}
}

// TestCreateBackendFlag: create accepts --backend like the other lifecycle
// verbs — the banner reflects the resolved backend, and a retired backend
// name is a clear migration error, never a silent fallback.
func TestCreateBackendFlag(t *testing.T) {
	project := newProject(t)
	fakeMsb(t)
	code, _, errs := run("create", "feat", "--src", project, "--backend", "microsandbox")
	if code != 0 {
		t.Fatalf("create --backend = %d, %s", code, errs)
	}
	if !strings.Contains(errs, "backend=microsandbox (msb)") {
		t.Errorf("configLine should show the resolved backend: %q", errs)
	}
	code, _, errs = run("enter", "feat", "--src", project, "--backend", "microvm")
	if code != 1 || !strings.Contains(errs, "microvm backend was removed") {
		t.Errorf("enter --backend microvm = (%d, %q), want the smolvm-removal migration error", code, errs)
	}
}

func TestRunCreateWithDomains(t *testing.T) {
	project := newProject(t)
	if code, _, errs := run("create", "feat", "--src", project, "--allow-domains", "a.com,b.com"); code != 0 {
		t.Fatalf("create with domains = %d, %s", code, errs)
	}
	runEnv, _ := os.ReadFile(stateDir(project, "_meta", "run.env"))
	if !strings.Contains(string(runEnv), "DOMAINS=a.com,b.com") {
		t.Errorf("create --allow-domains not applied:\n%s", runEnv)
	}
}

func TestRunProxyMode(t *testing.T) {
	if code, _, _ := run("_proxy", "--listen", "127.0.0.1:-1"); code != 1 {
		t.Errorf("_proxy bad addr exit = %d, want 1", code)
	}
}

func TestResolveTargetSelection(t *testing.T) {
	project := newProject(t)
	if code, _, errs := run("show", "--src", project); code != 1 || !strings.Contains(errs, "no sandbox selected") {
		t.Errorf("no-sandbox show = (%d, %q)", code, errs)
	}
	if code, _, _ := run("create", "only", "--src", project); code != 0 {
		t.Fatal("create failed")
	}
	if code, out, _ := run("show", "--src", project); code != 0 || !strings.Contains(out, "only") {
		t.Errorf("single-agent auto-select = (%d, %q)", code, out)
	}
}

func TestListMarkerAndJSONResultNoKeys(t *testing.T) {
	project := newProject(t)
	if code, _, _ := run("create", "feat", "--src", project); code != 0 {
		t.Fatal("create failed")
	}
	if code, _, _ := run("use", "feat", "--src", project); code != 0 {
		t.Fatal("use failed")
	}
	if code, out, _ := run("list", "--src", project); code != 0 || !strings.Contains(out, "*") {
		t.Errorf("list active marker = (%d, %q)", code, out)
	}
}
