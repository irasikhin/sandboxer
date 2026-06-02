package runner

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
)

func requireExec(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		if _, err := exec.LookPath(n); err != nil {
			t.Skipf("%s not available", n)
		}
	}
}

// --- pure helpers -----------------------------------------------------------

func TestExitCode(t *testing.T) {
	requireExec(t, "sh")
	if exitCode(nil) != 0 {
		t.Error("nil → 0")
	}
	if c := exitCode(exec.Command("sh", "-c", "exit 4").Run()); c != 4 {
		t.Errorf("exit 4 → %d", c)
	}
	if c := exitCode(exec.Command(filepath.Join(t.TempDir(), "nope")).Run()); c != 1 {
		t.Errorf("start failure → %d, want 1", c)
	}
}

func TestPosixQuote(t *testing.T) {
	if got := posixQuote("a b"); got != "'a b'" {
		t.Errorf("posixQuote = %q", got)
	}
	if got := posixQuote("it's"); got != `'it'\''s'` {
		t.Errorf("posixQuote with apostrophe = %q", got)
	}
}

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
		Defaults: config.Defaults{Agent: "claude", Backend: "native"},
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
		Defaults: config.Defaults{Agent: "claude", Backend: "native"},
		Stdout:   &out, Stderr: &errb,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Count != 1 {
		t.Errorf("Count = %d, want 1 (one sandbox failed to create)", res.Count)
	}
	if !strings.Contains(errb.String(), "make sandbox bad") {
		t.Errorf("missing failure note for the bad sandbox: %q", errb.String())
	}
}

func TestWrapLimits(t *testing.T) {
	// No mem/cpu/wall → just nice.
	s := launchSpec{nice: 7, slug: "x"}
	got := s.wrapLimits([]string{"echo", "hi"})
	want := []string{"nice", "-n", "7", "echo", "hi"}
	if !slices.Equal(got, want) {
		t.Errorf("wrapLimits default = %v, want %v", got, want)
	}
	// Wall + timeout available → timeout prefix before nice.
	if _, err := exec.LookPath("timeout"); err == nil {
		s2 := launchSpec{nice: 5, wall: "30", slug: "x"}
		got2 := s2.wrapLimits([]string{"echo"})
		if len(got2) < 3 || got2[0] != "timeout" || got2[1] != "--signal=TERM" || got2[2] != "30" {
			t.Errorf("wrapLimits wall = %v, want timeout prefix", got2)
		}
		if !slices.Contains(got2, "nice") {
			t.Errorf("wrapLimits wall should still nice: %v", got2)
		}
	}
}

func TestWrapLimitsSystemd(t *testing.T) {
	// With mem/cpu set and a (fake) systemd-run plus XDG_RUNTIME_DIR available,
	// the systemd-run --scope wrapper is used.
	bin := t.TempDir()
	writeScript(t, filepath.Join(bin, "systemd-run"), "exit 0\n")
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	s := launchSpec{nice: 10, slug: "x", mem: "2G", cpu: "50%"}
	got := strings.Join(s.wrapLimits([]string{"bash", "-c", "cmd"}), " ")
	for _, want := range []string{
		"systemd-run --user --scope", "MemoryMax=2G", "CPUQuota=50%",
		"TasksMax=1024", "nice -n 10", "bash -c cmd",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("wrapLimits systemd missing %q in %q", want, got)
		}
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
		Defaults:   config.Defaults{Agent: "claude", Backend: "native"},
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
		Defaults: config.Defaults{Agent: "claude", Backend: "native"},
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

func TestRunMissingTasksFile(t *testing.T) {
	if _, err := Run(Options{Src: t.TempDir(), Defaults: config.Defaults{Agent: "claude", Backend: "native"}, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}); err == nil {
		t.Error("missing tasks file should error")
	}
}

func TestRunNoTasks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "sandboxer.tasks"), []byte("# only comments\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Run(Options{Src: root, Defaults: config.Defaults{Agent: "claude", Backend: "native"}, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	if err == nil || !strings.Contains(err.Error(), "no tasks") {
		t.Errorf("empty tasks should error with 'no tasks', got %v", err)
	}
}

func TestRunUnknownAgent(t *testing.T) {
	root := t.TempDir()
	writeTasks(t, root)
	_, err := Run(Options{Src: root, Defaults: config.Defaults{Agent: "nope", Backend: "native"}, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
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
	_, err := Run(Options{ConfigPath: bad, Defaults: config.Defaults{Agent: "claude", Backend: "native"}, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	if err == nil || !strings.Contains(err.Error(), "load profile") {
		t.Errorf("bad profile should error, got %v", err)
	}
}
