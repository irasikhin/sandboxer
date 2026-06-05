package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/sandbox"
)

// TestRunAutoScaffold: create with no config writes a default sandboxer.yaml
// into the project root, announces it, and applies it (name = slug); opting out
// keeps the no-profile path.
func TestRunAutoScaffold(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	t.Setenv("SANDBOXER_PROFILES", t.TempDir())

	project := t.TempDir()
	code, _, errs := run("create", "feat", "--src", project)
	if code != 0 {
		t.Fatalf("create = %d (%q)", code, errs)
	}
	if !strings.Contains(errs, "scaffolded a default") {
		t.Errorf("auto-scaffold was not announced: %q", errs)
	}
	scaffold := filepath.Join(project, "sandboxer.yaml")
	doc, err := config.LoadDocument(scaffold)
	if err != nil {
		t.Fatalf("auto-scaffold did not parse: %v", err)
	}
	if p, _ := doc.Select(""); p.Name != "feat" {
		t.Errorf("scaffold name should match the slug, got %+v", p)
	}

	// Opt-out: no file written.
	t.Setenv("SANDBOXER_NO_SCAFFOLD", "1")
	other := t.TempDir()
	if code, _, _ := run("create", "x", "--src", other); code != 0 {
		t.Fatal("create with opt-out failed")
	}
	if fileExists(filepath.Join(other, "sandboxer.yaml")) {
		t.Error("SANDBOXER_NO_SCAFFOLD=1 should skip scaffolding")
	}
}

// TestRunInit covers scaffolding a starter sandboxer.yaml: it parses, refuses to
// clobber an existing file, and --force rewrites it.
func TestRunInit(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	t.Chdir(t.TempDir())

	if code, out, errs := run("init", "demo"); code != 0 || !strings.Contains(out, "wrote sandboxer.yaml") {
		t.Fatalf("init = (%d, %q, %q)", code, out, errs)
	}
	doc, err := config.LoadDocument("sandboxer.yaml")
	if err != nil {
		t.Fatalf("scaffold did not parse: %v", err)
	}
	p, err := doc.Select("")
	if err != nil || p.Name != "demo" || p.Agent == "" || p.Backend == "" {
		t.Errorf("scaffold profile wrong: %+v (err %v)", p, err)
	}
	// Refuses to overwrite without --force.
	if code, _, errs := run("init"); code != 1 || !strings.Contains(errs, "already exists") {
		t.Errorf("init over existing = (%d, %q), want refusal", code, errs)
	}
	// --force rewrites.
	if code, _, errs := run("init", "other", "--force"); code != 0 {
		t.Errorf("init --force = %d (%q)", code, errs)
	}
	doc2, _ := config.LoadDocument("sandboxer.yaml")
	if p2, _ := doc2.Select(""); p2.Name != "other" {
		t.Errorf("--force did not rewrite name: %+v", p2)
	}
}

// TestConfigLine checks the resolved-settings banner that create/enter/exec/show
// print so the user always sees what config was actually used.
func TestConfigLine(t *testing.T) {
	t.Setenv("SANDBOXER_NO_EGRESS", "")

	// Defaults, no profile: egress on with a domain count, profile=none.
	rt := config.Runtime{Agent: "claude", Backend: "native", Egress: true, Domains: []string{"a.com", "b.com"}}
	line := configLine(rt, "feat", nil, "native")
	for _, want := range []string{"feat —", "agent=claude", "backend=native", "model=default", "egress=on (2 domains)", "profile=none", "deps=0"} {
		if !strings.Contains(line, want) {
			t.Errorf("configLine missing %q in %q", want, line)
		}
	}

	// With a named profile and deps; egress off when not enabled.
	prof := &config.Profile{Name: "web", Deps: []string{"x", "y"}}
	line2 := configLine(config.Runtime{Agent: "opencode", Backend: "podman", Model: "gpt-5"}, "web", prof, "podman")
	for _, want := range []string{"profile=web", "deps=2", "egress=off", "model=gpt-5"} {
		if !strings.Contains(line2, want) {
			t.Errorf("configLine (profile) missing %q in %q", want, line2)
		}
	}

	// Disabled via env is called out explicitly.
	t.Setenv("SANDBOXER_NO_EGRESS", "1")
	if l := configLine(rt, "feat", nil, "native"); !strings.Contains(l, "SANDBOXER_NO_EGRESS") {
		t.Errorf("configLine should note env-disabled egress: %q", l)
	}
}

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
	requireExec(t, "sh")
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
	t.Setenv("SANDBOXER_IN_CONTAINER", "")

	project := t.TempDir() // where .sandboxer lives
	depRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(mkdirAll(t, filepath.Join(depRoot, "lib")), "dep.txt"), []byte("d\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(t.TempDir(), "p.yaml")
	yaml := "name: feat2\nroots: [" + depRoot + "]\ndeps:\n  - lib\n"
	if err := os.WriteFile(cfg, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	if code, out, errs := run("create", "--src", project, "--config", cfg); code != 0 || !strings.Contains(out, "created") {
		t.Fatalf("create with profile = (%d, %q, %q)", code, out, errs)
	}
	if _, err := os.Stat(filepath.Join(project, ".sandboxer", "feat2", "lib", "dep.txt")); err != nil {
		t.Errorf("dependency not pulled: %v", err)
	}
	if code, _, errs := run("pull", "--src", project, "--config", cfg); code != 0 {
		t.Errorf("pull = %d, %s", code, errs)
	}
	if code, _, errs := run("push", "--src", project, "--config", cfg); code != 0 {
		t.Errorf("push = %d, %s", code, errs)
	}
	if code, out, _ := run("show", "--src", project, "--config", cfg); code != 0 || !strings.Contains(out, "lib") {
		t.Errorf("show with profile = (%d, %q)", code, out)
	}
}

func mkdirAll(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRunUseClear(t *testing.T) {
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
	// push with no manifest is a no-op (nothing to return), like depsync.
	if code, out, _ := run("push"); code != 0 || !strings.Contains(out, "0 rw entries") {
		t.Errorf("push in-container (no manifest) = (%d, %q)", code, out)
	}
}

func TestRunBatchDryRun(t *testing.T) {
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

// TestRunBatchFailingAgentExitsNonzero: a batch where the agent exits non-zero
// must propagate a non-zero process exit (so scripts/CI see the failure), with
// the ok/failed tally on stdout.
func TestRunBatchFailingAgentExitsNonzero(t *testing.T) {
	requireExec(t, "bash", "nice", "sh")
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\nexit 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "sandboxer.tasks"), []byte("[alpha]\ndo a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errs := run("run", "--src", project, "--backend", "native")
	if code != 1 {
		t.Fatalf("failing batch exit = %d, want 1\nout:%s\nerr:%s", code, out, errs)
	}
	if !strings.Contains(out, "0 ok, 1 failed") {
		t.Errorf("missing failure tally on stdout: %q", out)
	}
}

func TestRmAllNonexistent(t *testing.T) {
	if code, out, _ := run("rm-all", filepath.Join(t.TempDir(), "sub")); code != 0 || !strings.Contains(out, "removed") {
		t.Errorf("rm-all nonexistent = (%d, %q)", code, out)
	}
}

func TestRunNativeNonClaudeRejected(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "sandboxer.tasks"), []byte("[a]\ndo a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errs := run("run", "--src", project, "--backend", "native", "--agent", "codex")
	if code != 1 || !strings.Contains(errs, "native backend has no OS sandbox") {
		t.Errorf("native+codex run = (%d, %q), want exit 1 with the guard message", code, errs)
	}
}

// TestRunMultiProfileSelect: a project sandboxer.yaml with a profiles: map —
// `create` picks the default section, `create <name>` picks that section, and
// the section inherits the shared defaults.
func TestRunMultiProfileSelect(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	t.Setenv("SANDBOXER_PROFILES", t.TempDir()) // isolate the global store
	project := t.TempDir()
	t.Chdir(project)
	body := `defaults:
  agent: claude
  network:
    allowedDomains: [api.anthropic.com]
profiles:
  web:
    backend: podman
  api:
    backend: native
    model: opus
default: web
`
	if err := os.WriteFile("sandboxer.yaml", []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// No name → the default section (web).
	if code, out, errs := run("create"); code != 0 || !strings.Contains(out, `"web"`) {
		t.Fatalf("create default = (%d, %q, %q)", code, out, errs)
	}
	// Named section → api, inheriting defaults' agent + domains.
	if code, out, errs := run("create", "api"); code != 0 || !strings.Contains(out, `"api"`) {
		t.Fatalf("create api = (%d, %q, %q)", code, out, errs)
	}
	pj, _ := os.ReadFile(filepath.Join(project, ".sandboxer", "_meta", "api.profile.json"))
	s := string(pj)
	for _, want := range []string{`"backend": "native"`, `"model": "opus"`, `"agent": "claude"`, "api.anthropic.com"} {
		if !strings.Contains(s, want) {
			t.Errorf("api profile.json missing %q in:\n%s", want, s)
		}
	}
}

func TestRunAutoDiscoversProfile(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	project := t.TempDir()
	t.Chdir(project)
	// A native profile in the cwd must be picked up without --config; otherwise the
	// default (podman) backend would be used and the banner would not say native.
	if err := os.WriteFile("sandboxer.yaml", []byte("name: disco\nbackend: native\nagent: claude\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("sandboxer.tasks", []byte("[a]\ndo a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errs := run("run", "--dry-run")
	if code != 0 {
		t.Fatalf("run = %d, %s", code, errs)
	}
	if !strings.Contains(out, "backend=native") {
		t.Errorf("auto-discovered sandboxer.yaml not applied (want backend=native):\n%s", out)
	}
}
