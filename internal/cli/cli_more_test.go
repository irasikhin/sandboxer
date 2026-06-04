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
	t.Setenv("SHELL", "true")
	if code, _, errs := run("enter", "fresh", "--src", project, "--backend", "native"); code != 0 {
		t.Fatalf("enter auto-create = %d, %s", code, errs)
	}
	if _, err := os.Stat(filepath.Join(project, ".sandboxer", "fresh")); err != nil {
		t.Errorf("enter did not create the sandbox: %v", err)
	}
}

func TestRunCreateWithDomains(t *testing.T) {
	project := newProject(t)
	if code, _, errs := run("create", "feat", "--src", project, "--allow-domains", "a.com,b.com"); code != 0 {
		t.Fatalf("create with domains = %d, %s", code, errs)
	}
	runEnv, _ := os.ReadFile(filepath.Join(project, ".sandboxer", "_meta", "run.env"))
	if !strings.Contains(string(runEnv), "DOMAINS=a.com,b.com") {
		t.Errorf("create --allow-domains not applied:\n%s", runEnv)
	}
}

func TestRunDiffAndPush(t *testing.T) {
	requireExec(t, "diff")
	project := t.TempDir()
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	depRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(depRoot, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(depRoot, "lib", "d.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(t.TempDir(), "p.yaml")
	yaml := "name: feat\nroots: [" + depRoot + "]\ndeps:\n  - lib\n"
	if err := os.WriteFile(cfg, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("create", "--src", project, "--config", cfg); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}

	// Edit the pulled copy.
	copyF := filepath.Join(project, ".sandboxer", "feat", "lib", "d.txt")
	if err := os.WriteFile(copyF, []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// diff shows the change against the origin.
	if code, out, _ := run("diff", "feat", "--src", project); code != 0 || !strings.Contains(out, "edited") {
		t.Errorf("diff = (%d, %q)", code, out)
	}
	// push returns the rw dep to its origin.
	if code, _, errs := run("push", "--src", project, "--config", cfg); code != 0 {
		t.Errorf("push = %d, %s", code, errs)
	}
	if got, _ := os.ReadFile(filepath.Join(depRoot, "lib", "d.txt")); string(got) != "edited\n" {
		t.Errorf("origin not updated by push: %q", got)
	}
}

// TestRunExecPushFailureExitsNonzero: if the post-exec copy-back fails, the
// command must exit non-zero (not silently report success) so the user never
// believes work was returned when it wasn't.
func TestRunExecPushFailureExitsNonzero(t *testing.T) {
	requireExec(t, "true")
	project := t.TempDir()
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	depRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(depRoot, "sub", "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(depRoot, "sub", "lib", "d.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(t.TempDir(), "p.yaml")
	yaml := "name: feat\nbackend: native\nagent: claude\nroots: [" + depRoot + "]\ndeps:\n  - sub/lib\n"
	if err := os.WriteFile(cfg, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("create", "--src", project, "--config", cfg); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}
	// Replace the origin's parent directory (depRoot/sub) with a regular file:
	// the copy-back's MkdirAll(parent-of-origin) then fails with ENOTDIR — a
	// perms-independent way to fail the push (works even when tests run as root).
	if err := os.RemoveAll(filepath.Join(depRoot, "sub")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(depRoot, "sub"), []byte("blocker\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, errs := run("exec", "--src", project, "--config", cfg, "--backend", "native", "--", "true")
	if code != 1 {
		t.Fatalf("exec with failing push exit = %d, want 1\nerr:%s", code, errs)
	}
	if !strings.Contains(errs, "push failed") {
		t.Errorf("missing push-failed diagnostic on stderr: %q", errs)
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
