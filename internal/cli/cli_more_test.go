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
	t.Setenv("SANDBOXER_ENGINE", "") // let DetectEngine find the fake podman
}

func TestRunEnterExecContainer(t *testing.T) {
	requireExec(t, "git", "rsync", "sh")
	project := newGitProject(t)
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
	requireExec(t, "git", "rsync", "sh")
	project := newGitProject(t)
	t.Setenv("SHELL", "true")
	// The sandbox does not exist yet → enter creates it first.
	if code, _, errs := run("enter", "fresh", "--src", project, "--backend", "native"); code != 0 {
		t.Fatalf("enter auto-create = %d, %s", code, errs)
	}
	if _, err := os.Stat(filepath.Join(project, ".sandboxer", "fresh")); err != nil {
		t.Errorf("enter did not create the sandbox: %v", err)
	}
}

func TestRunCreateWithDomains(t *testing.T) {
	requireExec(t, "git", "rsync")
	project := newGitProject(t)
	if code, _, errs := run("create", "feat", "--src", project, "--allow-domains", "a.com,b.com"); code != 0 {
		t.Fatalf("create with domains = %d, %s", code, errs)
	}
	runEnv, _ := os.ReadFile(filepath.Join(project, ".sandboxer", "_meta", "run.env"))
	if !strings.Contains(string(runEnv), "DOMAINS=a.com,b.com") {
		t.Errorf("create --allow-domains not applied:\n%s", runEnv)
	}
}

func TestRunMergeSuccessAndPatch(t *testing.T) {
	requireExec(t, "git", "rsync", "sh")
	project := newGitProject(t)
	if code, _, _ := run("create", "feat", "--src", project); code != 0 {
		t.Fatal("create failed")
	}
	// Produce a commit in the sandbox (exec auto-commits the work).
	if code, _, errs := run("exec", "feat", "--src", project, "--backend", "native", "--", "sh", "-c", "echo added > newfile.txt"); code != 0 {
		t.Fatalf("exec change = %d, %s", code, errs)
	}
	if code, out, errs := run("merge", "feat", "--src", project); code != 0 || !strings.Contains(out, "merged") {
		t.Errorf("merge success = (%d, %q, %q)", code, out, errs)
	}
	// And as patches.
	if code, out, _ := run("merge", "--patch", "feat", "--src", project); code != 0 || !strings.Contains(out, "patch[feat]") {
		t.Errorf("merge --patch = (%d, %q)", code, out)
	}
	if _, err := os.Stat(filepath.Join(project, ".sandboxer", "_patches", "feat")); err != nil {
		t.Errorf("patch dir not created: %v", err)
	}
}

func TestRunMergeNonGit(t *testing.T) {
	requireExec(t, "git", "rsync")
	isolateGit(t)
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	project := t.TempDir() // not a git repo
	if err := os.WriteFile(filepath.Join(project, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := run("create", "feat", "--src", project); code != 0 {
		t.Fatal("create failed")
	}
	if code, _, errs := run("merge", "feat", "--src", project); code != 1 || !strings.Contains(errs, "not a git repo") {
		t.Errorf("merge non-git = (%d, %q)", code, errs)
	}
}

func TestRunProxyMode(t *testing.T) {
	// The hidden _proxy mode runs the allowlist proxy; a bad listen addr makes
	// ListenAndServe return immediately with an error.
	if code, _, _ := run("_proxy", "--listen", "127.0.0.1:-1"); code != 1 {
		t.Errorf("_proxy bad addr exit = %d, want 1", code)
	}
}

func TestResolveTargetSelection(t *testing.T) {
	requireExec(t, "git", "rsync")
	project := newGitProject(t)

	// No sandbox, no current, zero agents → clear error.
	if code, _, errs := run("show", "--src", project); code != 1 || !strings.Contains(errs, "no sandbox selected") {
		t.Errorf("no-sandbox show = (%d, %q)", code, errs)
	}
	// A single sandbox is auto-selected when no slug is given.
	if code, _, _ := run("create", "only", "--src", project); code != 0 {
		t.Fatal("create failed")
	}
	if code, out, _ := run("show", "--src", project); code != 0 || !strings.Contains(out, "only") {
		t.Errorf("single-agent auto-select = (%d, %q)", code, out)
	}
}

func TestListMarkerAndJSONResultNoKeys(t *testing.T) {
	requireExec(t, "git", "rsync")
	project := newGitProject(t)
	if code, _, _ := run("create", "feat", "--src", project); code != 0 {
		t.Fatal("create failed")
	}
	if code, _, _ := run("use", "feat", "--src", project); code != 0 {
		t.Fatal("use failed")
	}
	// The active sandbox is marked with '*'.
	if code, out, _ := run("list", "--src", project); code != 0 || !strings.Contains(out, "*") {
		t.Errorf("list active marker = (%d, %q)", code, out)
	}

	// jsonResult returns "" for valid JSON without result/error keys.
	p := filepath.Join(t.TempDir(), "j.json")
	if err := os.WriteFile(p, []byte(`{"other":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := jsonResult(p); got != "" {
		t.Errorf("jsonResult without keys = %q, want empty", got)
	}
}
