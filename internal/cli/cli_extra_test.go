package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/sandbox"
)

func TestSilentErr(t *testing.T) {
	if (silentErr{errors.New("boom")}).Error() != "boom" {
		t.Error("silentErr.Error should pass through the wrapped message")
	}
}

func TestLoadStoredProfile(t *testing.T) {
	base, err := sandbox.ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if loadStoredProfile(base, "x") != nil {
		t.Error("no profile.json → nil")
	}
	if err := base.WriteProfileJSON("x", []byte(`{"name":"x","model":"m"}`)); err != nil {
		t.Fatal(err)
	}
	if p := loadStoredProfile(base, "x"); p == nil || p.Model != "m" {
		t.Errorf("stored profile = %+v, want model m", p)
	}
	if err := base.WriteProfileJSON("y", []byte("garbage")); err != nil {
		t.Fatal(err)
	}
	if loadStoredProfile(base, "y") != nil {
		t.Error("garbage profile.json → nil")
	}
}

func TestRunEnterExecNative(t *testing.T) {
	requireExec(t, "rsync", "sh")
	project := newProject(t)
	t.Setenv("SHELL", "true") // NativeEnter runs $SHELL; `true` exits 0

	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}
	if code, _, errs := run("enter", "feat", "--src", project, "--backend", "native"); code != 0 {
		t.Errorf("enter native = %d, %s", code, errs)
	}
	if code, _, errs := run("exec", "feat", "--src", project, "--backend", "native", "--", "sh", "-c", "exit 0"); code != 0 {
		t.Errorf("exec ok = %d, %s", code, errs)
	}
	if code, _, _ := run("exec", "feat", "--src", project, "--backend", "native", "--", "sh", "-c", "exit 5"); code != 1 {
		t.Errorf("exec non-zero exit code = %d, want 1", code)
	}
	if code, _, errs := run("exec", "feat", "--src", project, "--backend", "native"); code != 1 || !strings.Contains(errs, "command after --") {
		t.Errorf("exec without -- = (%d, %q)", code, errs)
	}
	if code, _, errs := run("exec", "missing", "--src", project, "--backend", "native", "--", "true"); code != 1 || !strings.Contains(errs, "no sandbox") {
		t.Errorf("exec missing sandbox = (%d, %q)", code, errs)
	}
}

func TestRunPullPushNoProfile(t *testing.T) {
	requireExec(t, "rsync")
	project := newProject(t)
	if code, _, _ := run("create", "feat", "--src", project); code != 0 {
		t.Fatal("create failed")
	}
	if code, _, errs := run("pull", "feat", "--src", project); code != 1 || !strings.Contains(errs, "no profile") {
		t.Errorf("pull without profile = (%d, %q)", code, errs)
	}
	if code, _, errs := run("push", "feat", "--src", project); code != 1 || !strings.Contains(errs, "no manifest") {
		t.Errorf("push without manifest = (%d, %q)", code, errs)
	}
}

func TestRunProfileFlow(t *testing.T) {
	requireExec(t, "rsync")
	t.Setenv("SANDBOXER_IN_CONTAINER", "")

	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "main.txt"), []byte("m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dep := filepath.Join(t.TempDir(), "lib")
	if err := os.MkdirAll(dep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dep, "dep.txt"), []byte("d\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(project, "sandboxer.yaml")
	yaml := "name: feat2\nmainSrc: " + project + "\nsrcs:\n  - from: " + dep + "\n    to: vendor\n    mode: rw\n"
	if err := os.WriteFile(cfg, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	if code, out, errs := run("create", "--config", cfg); code != 0 || !strings.Contains(out, "created") {
		t.Fatalf("create with profile = (%d, %q, %q)", code, out, errs)
	}
	if _, err := os.Stat(filepath.Join(project, ".sandboxer", "feat2", "vendor", "dep.txt")); err != nil {
		t.Errorf("dependency not vendored: %v", err)
	}
	if code, _, errs := run("pull", "--config", cfg); code != 0 {
		t.Errorf("pull = %d, %s", code, errs)
	}
	if code, _, errs := run("push", "--config", cfg); code != 0 {
		t.Errorf("push = %d, %s", code, errs)
	}
	if code, out, _ := run("show", "--config", cfg); code != 0 || !strings.Contains(out, "vendor") {
		t.Errorf("show with profile = (%d, %q)", code, out)
	}
}

func TestRunUseClear(t *testing.T) {
	requireExec(t, "rsync")
	project := newProject(t)
	if code, _, _ := run("create", "feat", "--src", project); code != 0 {
		t.Fatal("create failed")
	}
	if code, _, _ := run("use", "feat", "--src", project); code != 0 {
		t.Error("use set failed")
	}
	if code, out, _ := run("use", "--clear", "--src", project); code != 0 || !strings.Contains(out, "cleared") {
		t.Errorf("use --clear = (%d, %q)", code, out)
	}
	if code, out, _ := run("use", "--src", project); code != 0 || !strings.Contains(out, "no active sandbox") {
		t.Errorf("use get (none) = (%d, %q)", code, out)
	}
}

func TestRunInContainerInspect(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "1")
	t.Setenv("SANDBOXER_SANDBOX_DIR", t.TempDir())
	if code, out, _ := run("show"); code != 0 || !strings.Contains(out, "profile") {
		t.Errorf("show in-container = (%d, %q)", code, out)
	}
	if code, _, _ := run("pull"); code != 1 {
		t.Errorf("pull in-container (no profile.json) exit = %d, want 1", code)
	}
	if code, _, _ := run("push"); code != 1 {
		t.Errorf("push in-container (no manifest) exit = %d, want 1", code)
	}
}

func TestRunBatchDryRun(t *testing.T) {
	requireExec(t, "rsync")
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "sandboxer.tasks"), []byte("[alpha]\ndo a\n\n[beta]\ndo b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errs := run("run", "--src", project, "--dry-run", "--backend", "native")
	if code != 0 {
		t.Fatalf("run batch = %d, %s", code, errs)
	}
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
		t.Errorf("run batch list output missing slugs:\n%s", out)
	}
}

func TestRmAllNonexistent(t *testing.T) {
	if code, out, _ := run("rm-all", filepath.Join(t.TempDir(), "sub")); code != 0 || !strings.Contains(out, "removed") {
		t.Errorf("rm-all nonexistent = (%d, %q)", code, out)
	}
}
