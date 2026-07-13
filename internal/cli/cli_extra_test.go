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

// TestRunAutoScaffold: create with no config writes a default .sandboxer/config.yaml
// into the project's state dir, announces it, and applies it (name = slug);
// opting out keeps the no-profile path.
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
	scaffold := filepath.Join(project, config.StateDirName, config.ConfigFileName)
	doc, err := config.LoadDocument(scaffold)
	if err != nil {
		t.Fatalf("auto-scaffold did not parse: %v", err)
	}
	if p, _ := doc.Select(""); p.Name != "feat" {
		t.Errorf("scaffold name should match the slug, got %+v", p)
	}
	// Auto-scaffold wires the active image: section and the image.nix hook (same
	// as `init`), so the custom image works on the auto-scaffold path too.
	if p, _ := doc.Select(""); p.Image.Nix == "" {
		t.Errorf("auto-scaffold should wire an active image hook, got %+v", p.Image)
	}
	if !fileExists(filepath.Join(project, config.StateDirName, imageNixFileName)) {
		t.Errorf("auto-scaffold should write %s under %s", imageNixFileName, config.StateDirName)
	}

	// Opt-out: no file written, and create without a profile is refused.
	t.Setenv("SANDBOXER_NO_SCAFFOLD", "1")
	other := t.TempDir()
	if code, _, errs := run("create", "x", "--src", other); code != 1 || !strings.Contains(errs, "no profile") {
		t.Fatalf("create with opt-out and no profile = (%d, %q); want 'no profile' error", code, errs)
	}
	if fileExists(filepath.Join(other, config.StateDirName, config.ConfigFileName)) {
		t.Error("SANDBOXER_NO_SCAFFOLD=1 should skip scaffolding")
	}
}

// TestRunInit covers scaffolding a starter .sandboxer/config.yaml plus its
// .sandboxer/image.nix hook: both parse/exist under .sandboxer/, the image:
// section is wired active, init refuses to clobber either file, and --force
// rewrites them.
func TestRunInit(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	t.Chdir(t.TempDir())

	if code, out, errs := run("profile", "init", "demo"); code != 0 || !strings.Contains(out, "wrote "+config.ConfigPath()) {
		t.Fatalf("init = (%d, %q, %q)", code, out, errs)
	}
	doc, err := config.LoadDocument(config.ConfigPath())
	if err != nil {
		t.Fatalf("scaffold did not parse: %v", err)
	}
	p, err := doc.Select("")
	if err != nil || p.Name != "demo" || p.Agent == "" || p.Backend == "" {
		t.Errorf("scaffold profile wrong: %+v (err %v)", p, err)
	}
	// init also writes the image hook under .sandboxer/ and wires an active
	// image: section at it; its relative nix: resolves to .sandboxer/image.nix.
	if !fileExists(imageNixPath()) {
		t.Fatalf("init did not write %s", imageNixPath())
	}
	if nb, err := os.ReadFile(imageNixPath()); err != nil || !strings.Contains(string(nb), "{ pkgs }") {
		t.Errorf("%s missing the image-hook contract: %v", imageNixPath(), err)
	}
	if p.Image.Nix == "" {
		t.Errorf("scaffold should wire an active image hook, got %+v", p.Image)
	}
	wantNix, _ := filepath.Abs(imageNixPath())
	if p.Image.Nix != wantNix {
		t.Errorf("scaffolded image.nix should resolve under .sandboxer/: got %q, want %q", p.Image.Nix, wantNix)
	}
	// Refuses to overwrite the config without --force.
	if code, _, errs := run("profile", "init"); code != 1 || !strings.Contains(errs, "already exists") {
		t.Errorf("init over existing = (%d, %q), want refusal", code, errs)
	}
	// Refuses to clobber an existing image.nix even when the config is gone.
	if err := os.Remove(config.ConfigPath()); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("profile", "init"); code != 1 || !strings.Contains(errs, imageNixFileName) {
		t.Errorf("init over existing %s = (%d, %q), want refusal", imageNixPath(), code, errs)
	}
	// --force rewrites both.
	if code, _, errs := run("profile", "init", "other", "--force"); code != 0 {
		t.Errorf("init --force = %d (%q)", code, errs)
	}
	doc2, _ := config.LoadDocument(config.ConfigPath())
	if p2, _ := doc2.Select(""); p2.Name != "other" {
		t.Errorf("--force did not rewrite name: %+v", p2)
	}
}

// TestLegacyConfigHint covers the migration notice: it fires only when the
// stale root-level ./.sandboxer.yaml is present AND the new .sandboxer/config.yaml
// is not, and never inside the container.
func TestLegacyConfigHint(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	t.Chdir(t.TempDir())

	var buf strings.Builder
	// No legacy file → silent.
	legacyConfigHint(&buf, ".")
	if buf.String() != "" {
		t.Errorf("hint with no legacy file: %q", buf.String())
	}

	// Legacy file present, new one absent → hint fires.
	if err := os.WriteFile(config.LegacyConfigFileName, []byte("name: old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	legacyConfigHint(&buf, ".")
	if !strings.Contains(buf.String(), config.LegacyConfigFileName) || !strings.Contains(buf.String(), config.ConfigPath()) {
		t.Errorf("hint should name both paths: %q", buf.String())
	}

	// New config also present → no hint (already migrated).
	if err := os.MkdirAll(config.StateDirName, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.ConfigPath(), []byte("name: new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	legacyConfigHint(&buf, ".")
	if buf.String() != "" {
		t.Errorf("hint should be silent once migrated: %q", buf.String())
	}

	// Inside the container → always silent (old path is never read there anyway).
	t.Setenv("SANDBOXER_IN_CONTAINER", "1")
	if err := os.Remove(config.ConfigPath()); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	legacyConfigHint(&buf, ".")
	if buf.String() != "" {
		t.Errorf("hint should be silent in-container: %q", buf.String())
	}
}

// TestAutoScaffoldHintsLegacy: a create in a project with a stale root-level
// ./.sandboxer.yaml surfaces the migration hint before scaffolding the new
// default under .sandboxer/.
func TestAutoScaffoldHintsLegacy(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	t.Setenv("SANDBOXER_PROFILES", t.TempDir())
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, config.LegacyConfigFileName), []byte("name: old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errs := run("create", "feat", "--src", project)
	if code != 0 {
		t.Fatalf("create = %d (%q)", code, errs)
	}
	if !strings.Contains(errs, "legacy") || !strings.Contains(errs, config.LegacyConfigFileName) {
		t.Errorf("auto-scaffold should surface the legacy hint: %q", errs)
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

	// Chained mode: allowlist stays on, traffic routed through the proxy.
	up := config.Runtime{Agent: "claude", Backend: "docker", Egress: true, Proxy: "http://p:3128", Domains: []string{"a.com"}}
	if l := configLine(up, "feat", nil, "docker"); !strings.Contains(l, "egress=on→proxy (1 domains)") {
		t.Errorf("configLine chained-proxy branch: %q", l)
	}

	// Direct mode: egress off, the agent talks to the proxy directly.
	byp := config.Runtime{Agent: "claude", Backend: "docker", Proxy: "http://p:3128"}
	if l := configLine(byp, "feat", nil, "docker"); !strings.Contains(l, "egress=off → proxy (direct)") {
		t.Errorf("configLine direct-proxy branch: %q", l)
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
	if _, err := os.Stat(stateDir(project, "feat2", "workspace", "lib", "dep.txt")); err != nil {
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

// TestRunInContainerDenyAll: sandboxer is a host tool — inside the sandbox
// EVERY command refuses (deny-all), including the read-only ones that used to be
// allowed. The agent works on the vendored copies; data ops run on the host.
func TestRunInContainerDenyAll(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "1")
	t.Setenv("SANDBOXER_SANDBOX_DIR", t.TempDir())
	for _, cmd := range []string{"show", "pull", "push", "list", "diff"} {
		code, _, errs := run(cmd)
		if code != 1 || !strings.Contains(errs, "not available inside the sandbox") {
			t.Errorf("%s in-container = (%d, %q), want exit 1 deny-all", cmd, code, errs)
		}
	}
}

// TestCleanNonexistent: clean of a project with no state dir is a clean no-op
// (the state dir simply does not exist yet) — still reports the path it removed.
func TestCleanNonexistent(t *testing.T) {
	if code, out, _ := run("clean", "--force", filepath.Join(t.TempDir(), "sub")); code != 0 || !strings.Contains(out, "removed") {
		t.Errorf("clean nonexistent = (%d, %q)", code, out)
	}
}

// TestRunMultiProfileSelect: a project .sandboxer/config.yaml with a profiles:
// map — `create` picks the default section, `create <name>` picks that section,
// and the section inherits the shared defaults.
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
	if err := os.MkdirAll(config.StateDirName, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.ConfigPath(), []byte(body), 0o644); err != nil {
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
	pj, _ := os.ReadFile(stateDir(project, "_meta", "api.profile.json"))
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

// TestDoctorPopulated exercises the branches that need a populated environment:
// an agent found via its auth env var (not a config dir), a non-empty profile
// store, and a parseable config file in cwd.
func TestDoctorPopulated(t *testing.T) {
	// given: empty HOME (no agent auth-config dirs match) but a creds env var set,
	// a profile store holding one profile, and a valid config in cwd.
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "test-key") // claude → found via AuthEnv

	pd := t.TempDir()
	if err := os.WriteFile(filepath.Join(pd, "demo.yaml"), []byte("name: demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SANDBOXER_PROFILES", pd)

	cwd := t.TempDir()
	t.Chdir(cwd)
	if err := os.MkdirAll(config.StateDirName, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.ConfigPath(), []byte("name: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// when
	code, out, _ := run("doctor")

	// then
	if code != 0 {
		t.Fatalf("doctor = %d", code)
	}
	if !strings.Contains(out, "profile(s)") {
		t.Errorf("doctor output missing populated profile-store line:\n%s", out)
	}
	if !strings.Contains(out, "parses ok") {
		t.Errorf("doctor output missing config-parses-ok line:\n%s", out)
	}
}

// TestDoctorLegacyConfig: a stale root-level ./.sandboxer.yaml (no
// .sandboxer/config.yaml) is flagged as a legacy location to migrate.
func TestDoctorLegacyConfig(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	t.Setenv("SANDBOXER_PROFILES", t.TempDir())
	t.Chdir(t.TempDir())
	if err := os.WriteFile(config.LegacyConfigFileName, []byte("name: old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, out, _ := run("doctor")
	if code != 0 {
		t.Fatalf("doctor = %d", code)
	}
	if !strings.Contains(out, "legacy location") || !strings.Contains(out, config.LegacyConfigFileName) {
		t.Errorf("doctor should flag the legacy config location:\n%s", out)
	}
}

// TestDoctorConfigParseError covers the warn branch for an unparseable config
// file in cwd.
func TestDoctorConfigParseError(t *testing.T) {
	// given: a malformed config file in cwd
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	t.Setenv("SANDBOXER_PROFILES", t.TempDir())
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(config.StateDirName, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.ConfigPath(), []byte("name: [unterminated\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// when
	code, out, _ := run("doctor")

	// then: doctor never hard-fails, but the config line must NOT report "parses ok"
	if code != 0 {
		t.Fatalf("doctor = %d", code)
	}
	if !strings.Contains(out, config.ConfigPath()) {
		t.Errorf("doctor output missing project-config line:\n%s", out)
	}
	if strings.Contains(out, "parses ok") {
		t.Errorf("invalid config should not report 'parses ok':\n%s", out)
	}
}

// TestDoctorNoEngine covers the "no container engine" branch by running doctor
// with an empty PATH so engine detection finds nothing.
func TestDoctorNoEngine(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	t.Setenv("SANDBOXER_PROFILES", t.TempDir())
	t.Setenv("PATH", "") // no podman/docker discoverable

	code, out, _ := run("doctor")
	if code != 0 {
		t.Fatalf("doctor = %d", code)
	}
	if !strings.Contains(out, "container engine") || !strings.Contains(out, "⚠") {
		t.Errorf("doctor should warn about the missing engine:\n%s", out)
	}
}

func TestRunAutoDiscoversProfile(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	project := t.TempDir()
	t.Chdir(project)
	// A docker profile under .sandboxer/ must be picked up without --config;
	// otherwise the default (podman) backend would be used and the banner would
	// not say docker.
	if err := os.MkdirAll(config.StateDirName, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.ConfigPath(), []byte("name: disco\nbackend: docker\nagent: claude\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errs := run("create")
	if code != 0 {
		t.Fatalf("create = %d, %s", code, errs)
	}
	// The resolved-config banner (on stderr) must reflect the discovered profile.
	if !strings.Contains(errs, "backend=docker") {
		t.Errorf("auto-discovered %s not applied (want backend=docker):\n%s", config.ConfigPath(), errs)
	}
}
