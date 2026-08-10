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
	t.Setenv("SANDBOXER_SMOLVM", filepath.Join(dir, "no-smolvm")) // sweeps see msb alone
	t.Setenv("SANDBOXER_BACKEND", "microsandbox")
	t.Setenv("SANDBOXER_STATE", filepath.Join(dir, "state"))
	t.Setenv("SANDBOXER_IMAGE", "example.com/toolbox:test")
	t.Setenv("HOME", t.TempDir())
}

// fakeSmolvm is fakeMsb's smolvm twin, for the tests that drive the REAL
// backend lifecycle end to end: the microsandbox runner refuses shares under
// /tmp (where t.TempDir lives), while smolvm has no such restriction. Egress
// is routed through a proxy so `machine create` never tries to resolve the
// allowlist on the host.
func fakeSmolvm(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "smolvm")
	script := "#!/bin/sh\nif [ \"$1\" = machine ] && [ \"$2\" = ls ]; then echo '[]'; fi\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SANDBOXER_SMOLVM", bin)
	t.Setenv("SANDBOXER_MSB", filepath.Join(dir, "no-msb"))
	t.Setenv("SANDBOXER_BACKEND", "microvm")
	t.Setenv("SANDBOXER_STATE", filepath.Join(dir, "state"))
	t.Setenv("SANDBOXER_IMAGE", "example.com/toolbox:test")
	t.Setenv("SANDBOXER_PROXY", "http://proxy.test:3128")
	t.Setenv("HOME", t.TempDir())
}

func TestRunEnterExecMicrovm(t *testing.T) {
	requireExec(t, "sh")
	project := newProject(t)
	fakeSmolvm(t)

	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}
	if code, _, errs := run("enter", "feat", "--src", project, "--backend", "microvm"); code != 0 {
		t.Errorf("enter machine = %d, %s", code, errs)
	}
	if code, _, errs := run("exec", "feat", "--src", project, "--backend", "microvm", "--", "echo", "hi"); code != 0 {
		t.Errorf("exec machine = %d, %s", code, errs)
	}
}

func TestRunEnterAutoCreate(t *testing.T) {
	requireExec(t, "sh")
	project := newProject(t)
	fakeSmolvm(t)
	if code, _, errs := run("enter", "fresh", "--src", project); code != 0 {
		t.Fatalf("enter auto-create = %d, %s", code, errs)
	}
	if _, err := os.Stat(sandboxDir(project, "fresh")); err != nil {
		t.Errorf("enter did not create the sandbox: %v", err)
	}
}

// TestCreateBackendFlag: create accepts --backend like the other lifecycle
// verbs, and the banner reflects the override.
func TestCreateBackendFlag(t *testing.T) {
	project := newProject(t)
	fakeMsb(t)
	fakeSmolvmOnPath(t)
	code, _, errs := run("create", "feat", "--src", project, "--backend", "microvm")
	if code != 0 {
		t.Fatalf("create --backend = %d, %s", code, errs)
	}
	if !strings.Contains(errs, "backend=microvm") {
		t.Errorf("configLine should show the overridden backend: %q", errs)
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
