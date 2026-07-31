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

// TestRunAutoScaffold: create with no config writes a default sandboxer.nix
// at the project root, announces it, and applies it (name = slug); opting
// out keeps the no-profile path.
func TestRunAutoScaffold(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "")

	project := t.TempDir()
	gitInitProject(t, project)
	code, _, errs := run("create", "feat", "--src", project)
	if code != 0 {
		t.Fatalf("create = %d (%q)", code, errs)
	}
	if !strings.Contains(errs, "scaffolded a default") {
		t.Errorf("auto-scaffold was not announced: %q", errs)
	}
	scaffold := config.ConfigPathIn(project)
	doc, err := config.LoadDocument(scaffold)
	if err != nil {
		t.Fatalf("auto-scaffold did not parse: %v", err)
	}
	if p, _ := doc.Select(""); p.Name != "feat" {
		t.Errorf("scaffold name should match the slug, got %+v", p)
	}
	// The scaffold is ONE file: the stock image by default (the inline
	// image.hook is a commented example), so no second file appears.
	if p, _ := doc.Select(""); !p.Image.Empty() {
		t.Errorf("scaffold should default to the stock image, got %+v", p.Image)
	}
	if fileExists(filepath.Join(project, "sandboxer-image.nix")) {
		t.Error("the scaffold must not write a separate image-hook file")
	}

	// Opt-out: no file written, and create without a profile is refused.
	t.Setenv("SANDBOXER_NO_SCAFFOLD", "1")
	other := t.TempDir()
	if code, _, errs := run("create", "x", "--src", other); code != 1 || !strings.Contains(errs, "no profile") {
		t.Fatalf("create with opt-out and no profile = (%d, %q); want 'no profile' error", code, errs)
	}
	if fileExists(config.ConfigPathIn(other)) {
		t.Error("SANDBOXER_NO_SCAFFOLD=1 should skip scaffolding")
	}
}

// TestRunInit covers scaffolding a starter sandboxer.nix: it evaluates, the
// name lands, init refuses to clobber an existing config, and --force
// rewrites it.
func TestRunInit(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	t.Chdir(t.TempDir())

	if code, out, errs := run("config", "init", "demo"); code != 0 || !strings.Contains(out, "wrote "+config.ConfigPath()) {
		t.Fatalf("init = (%d, %q, %q)", code, out, errs)
	}
	doc, err := config.LoadDocument(config.ConfigPath())
	if err != nil {
		t.Fatalf("scaffold did not parse: %v", err)
	}
	p, err := doc.Select("")
	if err != nil || p.Name != "demo" || p.Backend == "" {
		t.Errorf("scaffold profile wrong: %+v (err %v)", p, err)
	}
	if len(p.Srcs) != 1 || p.Srcs[0].Src != "." {
		t.Errorf("scaffold should seed the explicit {src: .}: %+v", p.Srcs)
	}
	// Refuses to overwrite the config without --force.
	if code, _, errs := run("config", "init"); code != 1 || !strings.Contains(errs, "already exists") {
		t.Errorf("init over existing = (%d, %q), want refusal", code, errs)
	}
	// --force rewrites it.
	if code, _, errs := run("config", "init", "other", "--force"); code != 0 {
		t.Errorf("init --force = %d (%q)", code, errs)
	}
	doc2, _ := config.LoadDocument(config.ConfigPath())
	if p2, _ := doc2.Select(""); p2.Name != "other" {
		t.Errorf("--force did not rewrite name: %+v", p2)
	}
}

// TestLegacyConfigHint covers the migration notice: it fires only when a
// config at a retired location is present AND the new sandboxer.nix is not,
// and never inside the container.
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
	// The YAML-era sandboxer.yaml is recognized too (higher priority).
	if err := os.WriteFile(config.LegacyYAMLConfigFileName, []byte("name: old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	legacyConfigHint(&buf, ".")
	if !strings.Contains(buf.String(), config.LegacyYAMLConfigFileName) {
		t.Errorf("hint should name the yaml-era config: %q", buf.String())
	}

	// New config also present → no hint (already migrated).
	if err := os.WriteFile(config.ConfigPath(), []byte("{ name = \"new\"; }\n"), 0o644); err != nil {
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
// default at the project root.
func TestAutoScaffoldHintsLegacy(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	project := t.TempDir()
	gitInitProject(t, project)
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
	rt := config.Runtime{Backend: "docker", Egress: true, Domains: []string{"a.com", "b.com"}}
	line := configLine(rt, "feat", nil, "docker")
	for _, want := range []string{"sandboxer dev: feat —", "backend=docker", "egress=on (2 domains)", "profile=none", "srcs=0"} {
		if !strings.Contains(line, want) {
			t.Errorf("configLine missing %q in %q", want, line)
		}
	}

	// With a named profile and srcs; egress off AND no proxy is an OPEN network,
	// labelled distinctly (never the same "off" the trusted-proxy case uses).
	prof := &config.Profile{Name: "web", Srcs: []config.Src{{Src: "x"}, {Src: "y"}}}
	line2 := configLine(config.Runtime{Backend: "podman"}, "web", prof, "podman")
	for _, want := range []string{"profile=web", "srcs=2", "egress=OPEN"} {
		if !strings.Contains(line2, want) {
			t.Errorf("configLine (profile) missing %q in %q", want, line2)
		}
	}

	// Chained mode: allowlist stays on, traffic routed through the proxy.
	up := config.Runtime{Backend: "docker", Egress: true, Proxy: "http://p:3128", Domains: []string{"a.com"}}
	if l := configLine(up, "feat", nil, "docker"); !strings.Contains(l, "egress=on→proxy (1 domains)") {
		t.Errorf("configLine chained-proxy branch: %q", l)
	}

	// Direct mode: egress off, the agent talks to the proxy directly.
	byp := config.Runtime{Backend: "docker", Proxy: "http://p:3128"}
	if l := configLine(byp, "feat", nil, "docker"); !strings.Contains(l, "egress=off → proxy (direct)") {
		t.Errorf("configLine direct-proxy branch: %q", l)
	}

	// Disabled via env is called out explicitly (and is an OPEN network).
	t.Setenv("SANDBOXER_NO_EGRESS", "1")
	if l := configLine(rt, "feat", nil, "docker"); !strings.Contains(l, "SANDBOXER_NO_EGRESS") {
		t.Errorf("configLine should note env-disabled egress: %q", l)
	}
}

// TestWarnOpenNetwork: the open-network warning fires only when there is no
// allowlist sidecar and no proxy, and calls out hostConfigs when it is on.
func TestWarnOpenNetwork(t *testing.T) {
	t.Setenv("SANDBOXER_NO_EGRESS", "")
	var b strings.Builder
	// egress off, no proxy → OPEN; hostConfigs on → credential caveat.
	warnOpenNetwork(&b, config.Runtime{Backend: "docker"}, &config.Profile{HostConfigs: true})
	if !strings.Contains(b.String(), "WARNING") || !strings.Contains(b.String(), "hostConfigs") {
		t.Errorf("open network + hostConfigs should warn about creds: %q", b.String())
	}
	// A proxy is a boundary — no warning.
	b.Reset()
	warnOpenNetwork(&b, config.Runtime{Proxy: "http://p:3128"}, nil)
	if b.String() != "" {
		t.Errorf("proxy set should not warn: %q", b.String())
	}
	// Allowlist on — no warning.
	b.Reset()
	warnOpenNetwork(&b, config.Runtime{Egress: true, Domains: []string{"a.com"}}, nil)
	if b.String() != "" {
		t.Errorf("allowlist on should not warn: %q", b.String())
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
	if err := base.WriteProfileJSON("x", []byte(`{"name":"x","backend":"podman"}`)); err != nil {
		t.Fatal(err)
	}
	if p := loadStoredProfile(base, "x"); p == nil || p.Backend != "podman" {
		t.Errorf("stored profile = %+v, want backend podman", p)
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
// EVERY command refuses (deny-all), including the read-only ones. The agent
// works in the git worktree; sandboxer's own commands run on the host.
func TestRunInContainerDenyAll(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "1")
	t.Setenv("SANDBOXER_SANDBOX_DIR", t.TempDir())
	for _, cmd := range []string{"show", "list", "create", "enter", "exec"} {
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

// TestRunMultiProfileSelect: a project sandboxer.nix with a profiles
// map — `create` picks the default section, `create <name>` picks that
// self-contained section.
func TestRunMultiProfileSelect(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	project := t.TempDir()
	gitInitProject(t, project)
	t.Chdir(project)
	body := `{
  profiles = {
    web = { backend = "podman"; srcs = [ { src = "."; branch = "feat/x"; } ]; };
    api = {
      backend = "docker";
      session = "ephemeral";
      srcs = [ { src = "."; branch = "feat/x"; } ];
      egress.allowedDomains = [ "api.anthropic.com" ];
    };
  };
  default = "web";
}
`
	if err := os.WriteFile(config.ConfigPath(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// No name → the default section (web).
	if code, out, errs := run("create"); code != 0 || !strings.Contains(out, `"web"`) {
		t.Fatalf("create default = (%d, %q, %q)", code, out, errs)
	}
	// Named section → api, with its own backend/session/domains.
	if code, out, errs := run("create", "api"); code != 0 || !strings.Contains(out, `"api"`) {
		t.Fatalf("create api = (%d, %q, %q)", code, out, errs)
	}
	pj, _ := os.ReadFile(stateDir(project, "_meta", "api.profile.json"))
	s := string(pj)
	for _, want := range []string{`"backend": "docker"`, `"session": "ephemeral"`, "api.anthropic.com"} {
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
// and a parseable config file in cwd.
func TestDoctorPopulated(t *testing.T) {
	// given: empty HOME (no agent auth-config dirs match) but a creds env var
	// set, and a valid config in cwd.
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "test-key") // claude → found via AuthEnv

	cwd := t.TempDir()
	t.Chdir(cwd)
	if err := os.WriteFile(config.ConfigPath(), []byte("{ name = \"x\"; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// when
	code, out, _ := run("doctor")

	// then
	if code != 0 {
		t.Fatalf("doctor = %d", code)
	}
	if !strings.Contains(out, "parses ok") {
		t.Errorf("doctor output missing config-parses-ok line:\n%s", out)
	}
}

// TestDoctorLegacyConfig: a stale root-level ./.sandboxer.yaml (no
// sandboxer.nix) is flagged as a legacy location to migrate.
func TestDoctorLegacyConfig(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
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
	t.Chdir(t.TempDir())
	if err := os.WriteFile(config.ConfigPath(), []byte("{ name = broken\n"), 0o644); err != nil {
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

// TestDoctorGitRow: git is a hard prerequisite (every source is a worktree),
// so doctor reports it like nix and the engine.
func TestDoctorGitRow(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "")

	code, out, _ := run("doctor")
	if code != 0 {
		t.Fatalf("doctor = %d", code)
	}
	if !strings.Contains(out, "git") {
		t.Errorf("doctor output missing the git row:\n%s", out)
	}
}

// TestDoctorStrict: --strict turns warnings into a non-zero exit (CI
// preflight), without narrating anything beyond the existing tally.
func TestDoctorStrict(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	t.Setenv("PATH", "") // no git/nix/engine → warnings guaranteed

	code, out, errs := run("doctor", "--strict")
	if code != 1 {
		t.Fatalf("doctor --strict with warnings = %d, want 1", code)
	}
	if !strings.Contains(out, "warning(s)") {
		t.Errorf("doctor should still print the tally:\n%s", out)
	}
	if strings.Contains(errs, "sandboxer:") {
		t.Errorf("--strict must not add a second narration, got %q", errs)
	}
}

// TestDoctorNoEngine covers the "no container engine" branch by running doctor
// with an empty PATH so engine detection finds nothing.
func TestDoctorNoEngine(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
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
	gitInitProject(t, project)
	t.Chdir(project)
	// A docker profile in the project config must be picked up without --config;
	// otherwise the default (podman) backend would be used and the banner would
	// not say docker.
	if err := os.WriteFile(config.ConfigPath(), []byte("{ name = \"disco\"; backend = \"docker\"; srcs = [ { src = \".\"; branch = \"feat/x\"; } ]; }\n"), 0o644); err != nil {
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
