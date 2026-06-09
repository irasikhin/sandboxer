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

// TestRunAutoScaffold: create with no config writes a default .sandboxer.yaml
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
	scaffold := filepath.Join(project, config.ConfigFileName)
	doc, err := config.LoadDocument(scaffold)
	if err != nil {
		t.Fatalf("auto-scaffold did not parse: %v", err)
	}
	if p, _ := doc.Select(""); p.Name != "feat" {
		t.Errorf("scaffold name should match the slug, got %+v", p)
	}

	// Opt-out: no file written, and create without a profile is refused.
	t.Setenv("SANDBOXER_NO_SCAFFOLD", "1")
	other := t.TempDir()
	if code, _, errs := run("create", "x", "--src", other); code != 1 || !strings.Contains(errs, "no profile") {
		t.Fatalf("create with opt-out and no profile = (%d, %q); want 'no profile' error", code, errs)
	}
	if fileExists(filepath.Join(other, config.ConfigFileName)) {
		t.Error("SANDBOXER_NO_SCAFFOLD=1 should skip scaffolding")
	}
}

// TestRunInit covers scaffolding a starter .sandboxer.yaml: it parses, refuses to
// clobber an existing file, and --force rewrites it.
func TestRunInit(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	t.Chdir(t.TempDir())

	if code, out, errs := run("init", "demo"); code != 0 || !strings.Contains(out, "wrote "+config.ConfigFileName) {
		t.Fatalf("init = (%d, %q, %q)", code, out, errs)
	}
	doc, err := config.LoadDocument(config.ConfigFileName)
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
	doc2, _ := config.LoadDocument(config.ConfigFileName)
	if p2, _ := doc2.Select(""); p2.Name != "other" {
		t.Errorf("--force did not rewrite name: %+v", p2)
	}
}

// TestConfigLine checks the resolved-settings banner that create/enter/exec/show
// print so the user always sees what config was actually used.
func TestConfigLine(t *testing.T) {
	t.Setenv("SANDBOXER_NO_EGRESS", "")

	// Defaults, no profile: egress on with a domain count, profile=none.
	rt := config.Runtime{Agent: "claude", Backend: "docker", Egress: true, Domains: []string{"a.com", "b.com"}}
	line := configLine(rt, "feat", nil, "docker")
	for _, want := range []string{"feat —", "agent=claude", "backend=docker", "model=default", "egress=on (2 domains)", "profile=none", "deps=0"} {
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

	// Upstream-chaining mode: allowlist stays on, chained through a parent proxy.
	up := config.Runtime{Agent: "claude", Backend: "docker", Egress: true, UpstreamProxy: "http://p:3128", Domains: []string{"a.com"}}
	if l := configLine(up, "feat", nil, "docker"); !strings.Contains(l, "egress=on→upstream (1 domains)") {
		t.Errorf("configLine upstream branch: %q", l)
	}

	// Bypass-proxy mode: corporate proxy, the sidecar is off.
	byp := config.Runtime{Agent: "claude", Backend: "docker", HTTPProxy: "http://p:3128"}
	if l := configLine(byp, "feat", nil, "docker"); !strings.Contains(l, "egress=bypass-proxy") {
		t.Errorf("configLine bypass branch: %q", l)
	}

	// Disabled via env is called out explicitly.
	t.Setenv("SANDBOXER_NO_EGRESS", "1")
	if l := configLine(rt, "feat", nil, "docker"); !strings.Contains(l, "SANDBOXER_NO_EGRESS") {
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

// TestExecErrorPaths covers exec's two early failures, which are reported before
// any container engine is touched (so they need no docker/podman installed).
func TestExecErrorPaths(t *testing.T) {
	project := newProject(t)
	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}
	// exec without -- → "no command to run".
	if code, _, errs := run("exec", "feat", "--src", project, "--backend", "docker"); code != 1 || !strings.Contains(errs, "no command to run") {
		t.Errorf("exec without -- = (%d, %q)", code, errs)
	}
	// exec on a missing sandbox → "no sandbox".
	if code, _, errs := run("exec", "missing", "--src", project, "--backend", "docker", "--", "true"); code != 1 || !strings.Contains(errs, "no sandbox") {
		t.Errorf("exec missing sandbox = (%d, %q)", code, errs)
	}
}

func TestRunPullPushNoProfile(t *testing.T) {
	project := newProject(t)
	// Auto-scaffold fires and creates a profile; create succeeds.
	if code, _, _ := run("create", "feat", "--src", project); code != 0 {
		t.Fatal("create failed")
	}
	// Pull works because a profile exists (auto-scaffolded with no deps).
	if code, out, _ := run("pull", "feat", "--src", project); code != 0 {
		t.Errorf("pull after auto-scaffold = (%d, %q)", code, out)
	}
	// Push succeeds with 0 rw entries (an empty manifest was created).
	if code, out, _ := run("push", "feat", "--src", project); code != 0 || !strings.Contains(out, "0 rw entries") {
		t.Errorf("push (empty profile) = (%d, %q)", code, out)
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
	code, out, errs := run("run", "--src", project, "--dry-run", "--backend", "docker")
	if code != 0 {
		t.Fatalf("run batch = %d, %s", code, errs)
	}
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
		t.Errorf("run batch list output missing slugs:\n%s", out)
	}
}

func TestRmAllNonexistent(t *testing.T) {
	if code, out, _ := run("rm-all", "--force", filepath.Join(t.TempDir(), "sub")); code != 0 || !strings.Contains(out, "removed") {
		t.Errorf("rm-all nonexistent = (%d, %q)", code, out)
	}
}

// TestRunBackendNativeRejected: the removed native backend is rejected with a
// clear migration message instead of silently running a container.
func TestRunBackendNativeRejected(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "sandboxer.tasks"), []byte("[a]\ndo a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errs := run("run", "--src", project, "--backend", "native")
	if code != 1 || !strings.Contains(errs, "native backend was removed") {
		t.Errorf("native run = (%d, %q), want exit 1 with the removal message", code, errs)
	}
}

// TestRunMultiProfileSelect: a project .sandboxer.yaml with a profiles: map —
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
    backend: docker
    model: opus
default: web
`
	if err := os.WriteFile(config.ConfigFileName, []byte(body), 0o644); err != nil {
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
	for _, want := range []string{`"backend": "docker"`, `"model": "opus"`, `"agent": "claude"`, "api.anthropic.com"} {
		if !strings.Contains(s, want) {
			t.Errorf("api profile.json missing %q in:\n%s", want, s)
		}
	}
}

func TestCompletion(t *testing.T) {
	for _, sh := range []string{"bash", "zsh", "fish"} {
		code, out, _ := run("completion", sh)
		if code != 0 || out == "" {
			t.Errorf("completion %s = (%d, empty=%v)", sh, code, out == "")
		}
	}
	if code, _, _ := run("completion", "nope"); code != 1 {
		t.Errorf("completion nope = %d, want 1", code)
	}
	if code, _, _ := run("completion"); code != 1 {
		t.Errorf("completion (no arg) = %d, want 1", code)
	}
}

func TestDoctor(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	t.Setenv("SANDBOXER_PROFILES", t.TempDir())

	code, out, _ := run("doctor")
	if code != 0 {
		t.Fatalf("doctor = %d", code)
	}
	// Always reports the agent catalog (at least claude).
	if !strings.Contains(out, "claude") {
		t.Errorf("doctor output missing 'claude':\n%s", out)
	}
	if !strings.Contains(out, "ok") {
		t.Errorf("doctor output missing ok tally:\n%s", out)
	}
}

func TestRunAutoDiscoversProfile(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	project := t.TempDir()
	t.Chdir(project)
	// A docker profile in the cwd must be picked up without --config; otherwise the
	// default (podman) backend would be used and the banner would not say docker.
	if err := os.WriteFile(config.ConfigFileName, []byte("name: disco\nbackend: docker\nagent: claude\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("sandboxer.tasks", []byte("[a]\ndo a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errs := run("run", "--dry-run")
	if code != 0 {
		t.Fatalf("run = %d, %s", code, errs)
	}
	if !strings.Contains(out, "backend=docker") {
		t.Errorf("auto-discovered %s not applied (want backend=docker):\n%s", config.ConfigFileName, out)
	}
}
