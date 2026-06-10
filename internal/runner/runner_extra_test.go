package runner

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/toolbox"
)

func TestSmallHelpers(t *testing.T) {
	if orDefault("", "def") != "def" || orDefault("x", "def") != "x" {
		t.Error("orDefault")
	}
	if dryTag(true) != " DRY-RUN" || dryTag(false) != "" {
		t.Error("dryTag")
	}
	if got := limitsTag("2G", "100%", "30"); got != " mem=2G cpu=100% wall=30s" {
		t.Errorf("limitsTag = %q", got)
	}
	if limitsTag("", "", "") != "" {
		t.Error("empty limitsTag should be empty")
	}
}

func TestValidateLimits(t *testing.T) {
	for _, c := range []struct{ m, cpu, w string }{
		{"", "", ""}, {"512M", "100%", "1800"}, {"2G", "1.5", "30s"}, {"1g", "50%", "5m"},
	} {
		if err := validateLimits(c.m, c.cpu, c.w); err != nil {
			t.Errorf("validateLimits(%q,%q,%q) unexpected error: %v", c.m, c.cpu, c.w, err)
		}
	}
	for _, c := range []struct{ m, cpu, w string }{
		{"lots", "", ""}, {"", "fast", ""}, {"", "", "soon"}, {"", "2x", ""}, {"2GB!", "", ""},
	} {
		if err := validateLimits(c.m, c.cpu, c.w); err == nil {
			t.Errorf("validateLimits(%q,%q,%q) expected an error", c.m, c.cpu, c.w)
		}
	}
}

// TestRunBadLimitFailsFast: a malformed limit is rejected before any sandbox is
// created.
func TestRunBadLimitFailsFast(t *testing.T) {
	_, err := Run(Options{
		Src: t.TempDir(), Mem: "lots",
		Defaults: config.Defaults{Agent: "claude", Backend: "docker"},
		Stdout:   &bytes.Buffer{}, Stderr: &bytes.Buffer{},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid --mem") {
		t.Errorf("bad --mem should fail fast, got %v", err)
	}
}

// TestRunCountsLaunched: Result.Count reflects the sandboxes that actually
// launched, not the number of tasks, when one fails to be created.
func TestRunCountsLaunched(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, config.StateDirName)
	if err := os.MkdirAll(filepath.Join(state, "_meta"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Plant a regular file where the "bad" sandbox dir must go, so MakeSandbox
	// fails for that one task only.
	if err := os.WriteFile(filepath.Join(state, "bad"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tasks := filepath.Join(root, "sandboxer.tasks")
	if err := os.WriteFile(tasks, []byte("[ok]\ndo a thing\n[bad]\ndo another\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	res, err := Run(Options{
		Src: root, TasksFile: tasks, Keep: true, DryRun: true,
		Defaults: config.Defaults{Agent: "claude", Backend: "docker"},
		Stdout:   &out, Stderr: &errb,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Count != 1 {
		t.Errorf("Count = %d, want 1 (one sandbox failed to create)", res.Count)
	}
	if res.Failed != 1 {
		t.Errorf("Failed = %d, want 1 (the sandbox that could not be created)", res.Failed)
	}
	if !strings.Contains(errb.String(), "make sandbox bad") {
		t.Errorf("missing failure note for the bad sandbox: %q", errb.String())
	}
}

// TestRunWithProfile covers the profile-driven branches of Run: config load,
// agent from the profile, domain override and per-task profile JSON.
func TestRunWithProfile(t *testing.T) {
	root := t.TempDir()
	writeTasks(t, root)
	cfg := filepath.Join(t.TempDir(), "p.yaml")
	yaml := "agent: claude\nnetwork:\n  allowedDomains: [x.com]\n"
	if err := os.WriteFile(cfg, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errb bytes.Buffer
	res, err := Run(Options{
		Src:        root,
		ConfigPath: cfg,
		Defaults:   config.Defaults{Agent: "claude", Backend: "docker"},
		Overrides:  config.Overrides{Domains: "y.com"},
		DryRun:     true,
		Stdout:     &out,
		Stderr:     &errb,
	})
	if err != nil {
		t.Fatalf("Run with profile: %v\n%s", err, errb.String())
	}
	if res.Count != 2 || res.Root != root {
		t.Errorf("result = %+v", res)
	}
	// The override domain landed in run.env.
	runEnv, _ := os.ReadFile(filepath.Join(root, config.StateDirName, "_meta", "run.env"))
	if !strings.Contains(string(runEnv), "DOMAINS=y.com") {
		t.Errorf("run.env domains not overridden:\n%s", runEnv)
	}
	// Per-task profile JSON was stored.
	if _, err := os.Stat(filepath.Join(root, config.StateDirName, "_meta", "alpha.profile.json")); err != nil {
		t.Errorf("profile json not stored: %v", err)
	}
}

// --- Run orchestration ------------------------------------------------------

func writeTasks(t *testing.T, root string) {
	t.Helper()
	body := "[alpha]\ndo a\n\n[beta]\ndo b\n"
	if err := os.WriteFile(filepath.Join(root, "sandboxer.tasks"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunDryRun(t *testing.T) {
	root := t.TempDir()
	writeTasks(t, root)

	var out, errb bytes.Buffer
	res, err := Run(Options{
		Src:      root,
		Defaults: config.Defaults{Agent: "claude", Backend: "docker"},
		Image:    "img",
		MaxP:     2,
		DryRun:   true,
		Stdout:   &out,
		Stderr:   &errb,
	})
	if err != nil {
		t.Fatalf("Run dry: %v\nstderr=%s", err, errb.String())
	}
	if res.Count != 2 || res.Root != root {
		t.Errorf("result = %+v, want count 2 root %s", res, root)
	}
	if !strings.Contains(out.String(), "DRY-RUN") || !strings.Contains(out.String(), "all agents finished") {
		t.Errorf("summary output unexpected:\n%s", out.String())
	}
	// Each task produced a sandbox, a dry-run result log and a meta file.
	for _, slug := range []string{"alpha", "beta"} {
		if _, err := os.Stat(filepath.Join(root, config.StateDirName, slug)); err != nil {
			t.Errorf("sandbox %s not created: %v", slug, err)
		}
		resJSON, err := os.ReadFile(filepath.Join(root, config.StateDirName, "_logs", slug+".json"))
		if err != nil || !strings.Contains(string(resJSON), "dry-run") {
			t.Errorf("dry-run result for %s = %q, err=%v", slug, resJSON, err)
		}
		meta, _ := os.ReadFile(filepath.Join(root, config.StateDirName, "_meta", slug+".meta"))
		if !strings.Contains(string(meta), "exit=0") {
			t.Errorf("meta for %s = %q, want exit=0", slug, meta)
		}
	}
}

// TestRunDryRunLatestPins: a dry run resolves no engine, so an unresolved
// "latest" image rev fails fast with build-image guidance on a cold pins
// cache, and proceeds (with the variant tag) on a warm one.
func TestRunDryRunLatestPins(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	root := t.TempDir()
	writeTasks(t, root)
	cfg := filepath.Join(t.TempDir(), "p.yaml")
	if err := os.WriteFile(cfg, []byte("agent: claude\nimage:\n  nixpkgsRev: latest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := func() Options {
		return Options{
			Src: root, ConfigPath: cfg, DryRun: true,
			Defaults: config.Defaults{Agent: "claude", Backend: "docker"},
			Stdout:   &bytes.Buffer{}, Stderr: &bytes.Buffer{},
		}
	}

	if _, err := Run(opts()); err == nil || !strings.Contains(err.Error(), "build-image") {
		t.Errorf("cold-pins dry run = %v, want build-image guidance", err)
	}

	if err := toolbox.SavePins(toolbox.Pins{"nixpkgs": {Rev: strings.Repeat("a", 40)}}); err != nil {
		t.Fatal(err)
	}
	if res, err := Run(opts()); err != nil || res.Count != 2 {
		t.Errorf("warm-pins dry run = %+v, %v; want 2 launched", res, err)
	}
}

func TestRunMissingTasksFile(t *testing.T) {
	if _, err := Run(Options{Src: t.TempDir(), Defaults: config.Defaults{Agent: "claude", Backend: "docker"}, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}); err == nil {
		t.Error("missing tasks file should error")
	}
}

func TestRunNoTasks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sandboxer.tasks"), []byte("# only comments\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Run(Options{Src: root, Defaults: config.Defaults{Agent: "claude", Backend: "docker"}, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	if err == nil || !strings.Contains(err.Error(), "no tasks") {
		t.Errorf("empty tasks should error with 'no tasks', got %v", err)
	}
}

func TestRunUnknownAgent(t *testing.T) {
	root := t.TempDir()
	writeTasks(t, root)
	_, err := Run(Options{Src: root, Defaults: config.Defaults{Agent: "nope", Backend: "docker"}, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	if err == nil || !strings.Contains(err.Error(), "unknown agent") {
		t.Errorf("unknown agent should error, got %v", err)
	}
}

func TestRunEngineDetectionFails(t *testing.T) {
	root := t.TempDir()
	writeTasks(t, root)
	t.Setenv("PATH", t.TempDir()) // no podman/docker on PATH
	_, err := Run(Options{
		Src:       root,
		Defaults:  config.Defaults{Agent: "claude", Backend: "podman"},
		Overrides: config.Overrides{Backend: "podman"},
		Stdout:    &bytes.Buffer{}, Stderr: &bytes.Buffer{},
	})
	if err == nil || !strings.Contains(err.Error(), "podman or docker") {
		t.Errorf("missing engine should error, got %v", err)
	}
}

func TestRunBadProfile(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(bad, []byte("name: x\nbogus: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Run(Options{ConfigPath: bad, Defaults: config.Defaults{Agent: "claude", Backend: "docker"}, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	if err == nil || !strings.Contains(err.Error(), "load profile") {
		t.Errorf("bad profile should error, got %v", err)
	}
}

// TestRunUnknownToolPack: an unresolvable `tools:` pack fails the whole batch
// fast at spec resolution, before any sandbox or container work.
func TestRunUnknownToolPack(t *testing.T) {
	root := t.TempDir()
	writeTasks(t, root)
	cfg := filepath.Join(t.TempDir(), "p.yaml")
	if err := os.WriteFile(cfg, []byte("agent: claude\ntools: [nope]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Run(Options{
		Src: root, ConfigPath: cfg, DryRun: true,
		Defaults: config.Defaults{Agent: "claude", Backend: "docker"},
		Stdout:   &bytes.Buffer{}, Stderr: &bytes.Buffer{},
	})
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("unknown tool pack should fail the batch, got %v", err)
	}
}
