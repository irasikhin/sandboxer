package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
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

// TestResolveTargetGlobalDefaults wires resolveTarget through the global config:
// a project profile leaves agent unset, a global config provides it, and the
// resolved profile inherits the global default while the project value wins where
// both set it.
func TestResolveTargetGlobalDefaults(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	t.Setenv("SANDBOXER_PROFILES", t.TempDir())

	project := t.TempDir()
	// resolveProfileFile discovers .sandboxer/config.yaml relative to the cwd.
	t.Chdir(project)
	if err := os.MkdirAll(config.StateDirName, 0o755); err != nil {
		t.Fatal(err)
	}
	// Project: a multi-profile doc whose "web" section sets only the session mode.
	if err := os.WriteFile(config.ConfigPath(), []byte("profiles:\n  web:\n    session: ephemeral\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Global: defaults that the project inherits (backend).
	globalCfg := filepath.Join(t.TempDir(), "global.yaml")
	if err := os.WriteFile(globalCfg, []byte("defaults:\n  backend: podman\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SANDBOXER_CONFIG", globalCfg)

	tgt, err := resolveTarget(commonFlags{src: project}, "web")
	if err != nil {
		t.Fatalf("resolveTarget: %v", err)
	}
	if tgt.profile == nil {
		t.Fatal("resolveTarget returned a nil profile")
	}
	if tgt.profile.Backend != "podman" {
		t.Errorf("Backend = %q, want podman (from the global defaults)", tgt.profile.Backend)
	}
	if tgt.profile.Session != "ephemeral" {
		t.Errorf("Session = %q, want ephemeral (from the project profile)", tgt.profile.Session)
	}
	if tgt.slug != "web" {
		t.Errorf("slug = %q, want web", tgt.slug)
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
