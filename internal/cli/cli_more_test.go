package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakePodman puts a no-op `podman` engine on PATH and points HOME at an empty
// dir so no real credentials are bound.
func fakePodman(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "podman"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SANDBOXER_ENGINE", "")
}

func TestRunEnterExecContainer(t *testing.T) {
	requireExec(t, "sh")
	project := newProject(t)
	fakePodman(t)

	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}
	if code, _, errs := run("enter", "feat", "--src", project, "--backend", "podman"); code != 0 {
		t.Errorf("enter container = %d, %s", code, errs)
	}
	if code, _, errs := run("exec", "feat", "--src", project, "--backend", "podman", "--", "echo", "hi"); code != 0 {
		t.Errorf("exec container = %d, %s", code, errs)
	}
}

func TestRunEnterAutoCreate(t *testing.T) {
	requireExec(t, "sh")
	project := newProject(t)
	fakePodman(t)
	if code, _, errs := run("enter", "fresh", "--src", project, "--backend", "podman"); code != 0 {
		t.Fatalf("enter auto-create = %d, %s", code, errs)
	}
	if _, err := os.Stat(stateDir(project, "fresh")); err != nil {
		t.Errorf("enter did not create the sandbox: %v", err)
	}
}

// TestCreateBackendFlag: create accepts --backend like the other lifecycle
// verbs, and the banner reflects the override.
func TestCreateBackendFlag(t *testing.T) {
	project := newProject(t)
	fakePodman(t)
	code, _, errs := run("create", "feat", "--src", project, "--backend", "podman")
	if code != 0 {
		t.Fatalf("create --backend = %d, %s", code, errs)
	}
	if !strings.Contains(errs, "backend=podman") {
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
	p := filepath.Join(t.TempDir(), "j.json")
	if err := os.WriteFile(p, []byte(`{"other":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := jsonResult(p); got != "" {
		t.Errorf("jsonResult without keys = %q, want empty", got)
	}
}
