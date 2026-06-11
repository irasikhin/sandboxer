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
	requireExec(t, "sh")
	project := t.TempDir()
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	fakePodman(t) // the container "runs" and exits 0; only the copy-back fails
	depRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(depRoot, "sub", "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(depRoot, "sub", "lib", "d.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(t.TempDir(), "p.yaml")
	yaml := "name: feat\nbackend: podman\nagent: claude\nroots: [" + depRoot + "]\ndeps:\n  - sub/lib\n"
	if err := os.WriteFile(cfg, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("create", "--src", project, "--config", cfg); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}
	// Replace the manifest file with a directory: the copy-back's manifest read
	// then fails with EISDIR — a perms-independent way to fail the push (works
	// even when tests run as root). (Sabotaging the origin no longer fails the
	// push: a changed/unreadable origin is now SKIPped by the safe default.)
	mf := filepath.Join(project, ".sandboxer", "_meta", "feat.manifest.json")
	if err := os.RemoveAll(mf); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(mf, 0o755); err != nil {
		t.Fatal(err)
	}

	code, _, errs := run("exec", "--src", project, "--config", cfg, "--backend", "podman", "--", "true")
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
	// Project: a multi-profile doc whose "web" section sets only the model.
	if err := os.WriteFile(config.ConfigPath(), []byte("profiles:\n  web:\n    model: sonnet\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Global: defaults that the project inherits (agent) plus one the project
	// overrides via its own defaults (backend).
	globalCfg := filepath.Join(t.TempDir(), "global.yaml")
	if err := os.WriteFile(globalCfg, []byte("defaults:\n  agent: codex\n  backend: podman\n"), 0o644); err != nil {
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
	if tgt.profile.Agent != "codex" {
		t.Errorf("Agent = %q, want codex (inherited from the global defaults)", tgt.profile.Agent)
	}
	if tgt.profile.Backend != "podman" {
		t.Errorf("Backend = %q, want podman (from the global defaults)", tgt.profile.Backend)
	}
	if tgt.profile.Model != "sonnet" {
		t.Errorf("Model = %q, want sonnet (from the project profile)", tgt.profile.Model)
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
