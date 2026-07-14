package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
)

// isolateGlobals keeps a config-command test away from the host's named-profile
// store and global config (newProject does the same for project-based tests).
func isolateGlobals(t *testing.T) {
	t.Helper()
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	t.Setenv("SANDBOXER_PROFILES", t.TempDir())
	t.Setenv("SANDBOXER_CONFIG", filepath.Join(t.TempDir(), "global.yaml"))
}

// TestConfigSetGetUnsetFlat: the end-to-end round trip on a scaffolded flat
// config — set edits in place (scaffold comments survive), get reads the
// value back, unset removes it.
func TestConfigSetGetUnsetFlat(t *testing.T) {
	isolateGlobals(t)
	t.Chdir(t.TempDir())
	if code, _, errs := run("config", "init", "demo"); code != 0 {
		t.Fatalf("config init = %d, %s", code, errs)
	}

	if code, out, errs := run("config", "set", "backend", "podman"); code != 0 || !strings.Contains(out, "set backend (profile demo)") {
		t.Fatalf("config set = (%d, %q, %q)", code, out, errs)
	}
	body, err := os.ReadFile(config.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, comment := range []string{
		"# yaml-language-server:",
		"# Isolation backend: docker | podman.",
		"# Egress allowlist:",
		"# One-time setup script",
	} {
		if !strings.Contains(string(body), comment) {
			t.Errorf("scaffold comment lost after set: %q\n%s", comment, body)
		}
	}
	if code, out, _ := run("config", "get", "backend"); code != 0 || strings.TrimSpace(out) != "podman" {
		t.Errorf("config get backend = (%d, %q), want podman", code, out)
	}

	// env.<NAME> addressing.
	if code, _, errs := run("config", "set", "env.NODE_ENV", "production"); code != 0 {
		t.Fatalf("set env.NODE_ENV = %d, %s", code, errs)
	}
	if code, out, _ := run("config", "get", "env.NODE_ENV"); code != 0 || strings.TrimSpace(out) != "production" {
		t.Errorf("get env.NODE_ENV = (%d, %q)", code, out)
	}

	// A compound value prints as YAML.
	if code, _, errs := run("config", "set", "deps", "[src/lib, docs]"); code != 0 {
		t.Fatalf("set deps = %d, %s", code, errs)
	}
	if code, out, _ := run("config", "get", "deps"); code != 0 || !strings.Contains(out, "- src/lib") {
		t.Errorf("get deps = (%d, %q)", code, out)
	}

	if code, out, _ := run("config", "unset", "backend"); code != 0 || !strings.Contains(out, "unset backend") {
		t.Errorf("config unset = (%d, %q)", code, out)
	}
	if code, _, errs := run("config", "get", "backend"); code != 1 || !strings.Contains(errs, "not set") {
		t.Errorf("get after unset = (%d, %q), want not-set error", code, errs)
	}
	if code, _, errs := run("config", "unset", "backend"); code != 1 || !strings.Contains(errs, "not set") {
		t.Errorf("double unset = (%d, %q), want not-set error", code, errs)
	}

	// get with no key dumps the merged profile as YAML.
	if code, out, _ := run("config", "get"); code != 0 || !strings.Contains(out, "name: demo") || !strings.Contains(out, "NODE_ENV: production") {
		t.Errorf("config get dump = (%d, %q)", code, out)
	}
}

// TestConfigSetRejectsBadInput: the pre-write gate — a bad type, an unknown
// or removed key, or a field-local validation failure never lands on disk.
func TestConfigSetRejectsBadInput(t *testing.T) {
	isolateGlobals(t)
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(config.StateDirName, 0o755); err != nil {
		t.Fatal(err)
	}
	orig := "name: demo\nbackend: docker # keep\n"
	if err := os.WriteFile(config.ConfigPath(), []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		args    []string
		errPart string
	}{
		{[]string{"limits.pids", "notanint"}, "refusing to write"},     // strict re-decode (type)
		{[]string{"bogus", "1"}, "unknown key"},                        // unknown key
		{[]string{"sesion", "x"}, "did you mean"},                      // typo suggestion
		{[]string{"proxy", "http://x"}, "network.proxy"},               // removed-key hint
		{[]string{"network.allowedDomains", "[nodot]"}, "missing dot"}, // ValidateDomains gate
		{[]string{"image.llmAgentsRev", "abc"}, "40-char"},             // ValidateImageSpec gate
		{[]string{"deps", "[unclosed"}, "invalid value"},               // value parse error
		{[]string{"env.A.B", "x"}, "dot-free"},                         // dotted env name
		{[]string{"backend.sub", "x"}, "unknown key"},                  // path into a scalar
	} {
		code, _, errs := run(append([]string{"config", "set"}, tt.args...)...)
		if code != 1 || !strings.Contains(errs, tt.errPart) {
			t.Errorf("set %v = (%d, %q), want exit 1 with %q", tt.args, code, errs, tt.errPart)
		}
	}
	body, err := os.ReadFile(config.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != orig {
		t.Errorf("a rejected set modified the file:\n%s", body)
	}
}

// TestConfigTargetingMulti: section selection on a profiles: file — -p wins,
// then the active sandbox (sandboxer use), then default:.
func TestConfigTargetingMulti(t *testing.T) {
	project := newProject(t)
	cfg := config.ConfigPathIn(project)
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	multi := "profiles:\n  web:\n    backend: docker\n  api:\n    backend: docker\ndefault: web\n"
	if err := os.WriteFile(cfg, []byte(multi), 0o644); err != nil {
		t.Fatal(err)
	}

	// No selector → the file's default:.
	if code, out, errs := run("config", "set", "env.A", "1", "--src", project); code != 0 || !strings.Contains(out, "(profile web)") {
		t.Errorf("default-fallback set = (%d, %q, %q)", code, out, errs)
	}
	// -p targets the named section.
	if code, out, _ := run("config", "set", "env.B", "2", "-p", "api", "--src", project); code != 0 || !strings.Contains(out, "(profile api)") {
		t.Errorf("-p set = (%d, %q)", code, out)
	}
	// The active sandbox (use) beats default:.
	if code, _, errs := run("use", "api", "--src", project); code != 0 {
		t.Fatalf("use api = %d, %s", code, errs)
	}
	if code, out, _ := run("config", "set", "env.C", "3", "--src", project); code != 0 || !strings.Contains(out, "(profile api)") {
		t.Errorf("active-slug set = (%d, %q)", code, out)
	}
	// -p naming an absent section errors with the section list, creates nothing.
	if code, _, errs := run("config", "set", "env.X", "1", "-p", "nosuch", "--src", project); code != 1 || !strings.Contains(errs, "api, web") {
		t.Errorf("absent -p = (%d, %q)", code, errs)
	}

	doc, err := config.LoadDocument(cfg)
	if err != nil {
		t.Fatal(err)
	}
	web, _ := doc.Select("web")
	api, _ := doc.Select("api")
	if web.Env["A"] != "1" || web.Env["C"] != "" {
		t.Errorf("web env = %v", web.Env)
	}
	if api.Env["B"] != "2" || api.Env["C"] != "3" {
		t.Errorf("api env = %v", api.Env)
	}
	if doc.Has("nosuch") {
		t.Error("a failed -p set created the section")
	}

	// get resolves the same targeting.
	if code, out, _ := run("config", "get", "env.C", "--src", project); code != 0 || strings.TrimSpace(out) != "3" {
		t.Errorf("active-slug get = (%d, %q)", code, out)
	}
}

// TestConfigTargetingEdges: sole-profile fallback, the no-selector error, and
// a -p mismatch on a flat file.
func TestConfigTargetingEdges(t *testing.T) {
	isolateGlobals(t)
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(config.StateDirName, 0o755); err != nil {
		t.Fatal(err)
	}

	// Sole profile, no default: → picked.
	if err := os.WriteFile(config.ConfigPath(), []byte("profiles:\n  only:\n    backend: docker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := run("config", "set", "env.A", "1"); code != 0 || !strings.Contains(out, "(profile only)") {
		t.Errorf("sole-profile set = (%d, %q)", code, out)
	}

	// Two profiles, no default, no active → actionable error.
	if err := os.WriteFile(config.ConfigPath(), []byte("profiles:\n  a:\n    backend: docker\n  b:\n    backend: docker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("config", "set", "env.A", "1"); code != 1 || !strings.Contains(errs, "name a profile with -p") {
		t.Errorf("ambiguous set = (%d, %q)", code, errs)
	}

	// Flat file: -p must match the single profile's name.
	if err := os.WriteFile(config.ConfigPath(), []byte("name: demo\nbackend: docker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("config", "set", "env.A", "1", "-p", "other"); code != 1 || !strings.Contains(errs, "single profile") {
		t.Errorf("flat -p mismatch = (%d, %q)", code, errs)
	}
	if code, out, _ := run("config", "set", "env.A", "1", "-p", "demo"); code != 0 || !strings.Contains(out, "(profile demo)") {
		t.Errorf("flat -p match = (%d, %q)", code, out)
	}

	// Missing project config → scaffold hint, not an auto-scaffold.
	t.Chdir(t.TempDir())
	if code, _, errs := run("config", "set", "backend", "podman"); code != 1 || !strings.Contains(errs, "sandboxer config init") {
		t.Errorf("missing config set = (%d, %q)", code, errs)
	}
	if fileExists(config.ConfigPath()) {
		t.Error("a failed set scaffolded a config")
	}
}

// TestConfigGlobal: --global reads/writes the global config's defaults:,
// creating the file on first set; a project get sees the inherited value and
// unset explains inheritance.
func TestConfigGlobal(t *testing.T) {
	isolateGlobals(t)
	t.Chdir(t.TempDir())
	globalPath := os.Getenv("SANDBOXER_CONFIG")

	// get/unset need an existing file; set creates it.
	if code, _, errs := run("config", "get", "--global", "egress"); code != 1 || !strings.Contains(errs, "config set --global") {
		t.Errorf("global get missing = (%d, %q)", code, errs)
	}
	if code, out, errs := run("config", "set", "--global", "egress", "false"); code != 0 || !strings.Contains(out, "(defaults)") {
		t.Fatalf("global set = (%d, %q, %q)", code, out, errs)
	}
	body, err := os.ReadFile(globalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "# sandboxer global config") || !strings.Contains(string(body), "defaults:") {
		t.Errorf("fresh global file missing header/defaults:\n%s", body)
	}
	if code, out, _ := run("config", "get", "--global", "egress"); code != 0 || strings.TrimSpace(out) != "false" {
		t.Errorf("global get = (%d, %q)", code, out)
	}

	// A project profile inherits the global default and get shows the merge.
	if err := os.MkdirAll(config.StateDirName, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.ConfigPath(), []byte("name: demo\nbackend: docker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := run("config", "get", "egress"); code != 0 || strings.TrimSpace(out) != "false" {
		t.Errorf("merged get = (%d, %q), want the global egress", code, out)
	}
	// unset can't remove an inherited key from the project file — say so.
	if code, _, errs := run("config", "unset", "egress"); code != 1 || !strings.Contains(errs, "inherited") {
		t.Errorf("unset inherited = (%d, %q)", code, errs)
	}

	// A profiles: section in the global file is addressable with --global -p.
	if err := os.WriteFile(globalPath,
		[]byte("defaults:\n  egress: false\nprofiles:\n  shared:\n    backend: podman\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := run("config", "get", "--global", "-p", "shared", "backend"); code != 0 || strings.TrimSpace(out) != "podman" {
		t.Errorf("global -p get = (%d, %q)", code, out)
	}
	if code, out, _ := run("config", "set", "--global", "-p", "shared", "env.G", "1"); code != 0 || !strings.Contains(out, "(profile shared)") {
		t.Errorf("global -p set = (%d, %q)", code, out)
	}
	if code, _, errs := run("config", "set", "--global", "-p", "nosuch", "env.G", "1"); code != 1 || !strings.Contains(errs, "no profile") {
		t.Errorf("global -p absent = (%d, %q)", code, errs)
	}
	if code, out, _ := run("config", "unset", "--global", "egress"); code != 0 || !strings.Contains(out, "(defaults)") {
		t.Errorf("global unset = (%d, %q)", code, out)
	}
}

// TestConfigExplicitFile: -f targets any config file directly (e.g. a stored
// named profile).
func TestConfigExplicitFile(t *testing.T) {
	isolateGlobals(t)
	t.Chdir(t.TempDir())
	file := filepath.Join(t.TempDir(), "web.yaml")
	if err := os.WriteFile(file, []byte("name: web\nbackend: docker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, errs := run("config", "set", "-f", file, "backend", "podman"); code != 0 || !strings.Contains(out, file) {
		t.Fatalf("set -f = (%d, %q, %q)", code, out, errs)
	}
	if code, out, _ := run("config", "get", "-f", file, "backend"); code != 0 || strings.TrimSpace(out) != "podman" {
		t.Errorf("get -f = (%d, %q)", code, out)
	}
}

// TestConfigSetAnchoredSectionNote: editing an anchored profile section warns
// that aliasing profiles inherit the change.
func TestConfigSetAnchoredSectionNote(t *testing.T) {
	isolateGlobals(t)
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(config.StateDirName, 0o755); err != nil {
		t.Fatal(err)
	}
	multi := "profiles:\n  api: &api\n    backend: docker\n  api-prod:\n    <<: *api\n    session: ephemeral\ndefault: api\n"
	if err := os.WriteFile(config.ConfigPath(), []byte(multi), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errs := run("config", "set", "env.A", "1", "-p", "api")
	if code != 0 {
		t.Fatalf("anchored set = %d, %s", code, errs)
	}
	if !strings.Contains(errs, "anchored (&api)") {
		t.Errorf("expected an anchor note on stderr, got %q", errs)
	}
}
