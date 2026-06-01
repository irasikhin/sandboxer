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
	requireExec(t, "rsync", "sh")
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
	requireExec(t, "rsync", "sh")
	project := newProject(t)
	t.Setenv("SHELL", "true")
	if code, _, errs := run("enter", "fresh", "--src", project, "--backend", "native"); code != 0 {
		t.Fatalf("enter auto-create = %d, %s", code, errs)
	}
	if _, err := os.Stat(filepath.Join(project, ".sandboxer", "fresh")); err != nil {
		t.Errorf("enter did not create the sandbox: %v", err)
	}
}

func TestRunCreateWithDomains(t *testing.T) {
	requireExec(t, "rsync")
	project := newProject(t)
	if code, _, errs := run("create", "feat", "--src", project, "--allow-domains", "a.com,b.com"); code != 0 {
		t.Fatalf("create with domains = %d, %s", code, errs)
	}
	runEnv, _ := os.ReadFile(filepath.Join(project, ".sandboxer", "_meta", "run.env"))
	if !strings.Contains(string(runEnv), "DOMAINS=a.com,b.com") {
		t.Errorf("create --allow-domains not applied:\n%s", runEnv)
	}
}

func TestRunReturnAliasAndForce(t *testing.T) {
	requireExec(t, "rsync")
	project := newProject(t)
	if code, _, _ := run("create", "feat", "--src", project); code != 0 {
		t.Fatal("create failed")
	}
	copyFile := filepath.Join(project, ".sandboxer", "feat", "f.txt")
	if err := os.WriteFile(copyFile, []byte("sandbox-edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The source file changed out-of-band → plain return SKIPs it.
	if err := os.WriteFile(filepath.Join(project, "f.txt"), []byte("external\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// `merge` is a kept alias for `return`.
	if code, out, _ := run("merge", "feat", "--src", project); code != 0 || !strings.Contains(out, "SKIP") {
		t.Errorf("merge alias / SKIP = (%d, %q)", code, out)
	}
	if got, _ := os.ReadFile(filepath.Join(project, "f.txt")); string(got) != "external\n" {
		t.Errorf("source overwritten despite SKIP: %q", got)
	}
	// --force overwrites.
	if code, out, _ := run("return", "feat", "--src", project, "--force"); code != 0 || !strings.Contains(out, "RETURN") {
		t.Errorf("return --force = (%d, %q)", code, out)
	}
	if got, _ := os.ReadFile(filepath.Join(project, "f.txt")); string(got) != "sandbox-edit\n" {
		t.Errorf("source after --force = %q", got)
	}
}

func TestRunProxyMode(t *testing.T) {
	if code, _, _ := run("_proxy", "--listen", "127.0.0.1:-1"); code != 1 {
		t.Errorf("_proxy bad addr exit = %d, want 1", code)
	}
}

func TestResolveTargetSelection(t *testing.T) {
	requireExec(t, "rsync")
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
	requireExec(t, "rsync")
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
