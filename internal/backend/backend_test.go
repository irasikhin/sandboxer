package backend

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
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

// writeEngineScript writes an executable stub that appends each invocation's
// argv to logPath and exits with $SBX_EXIT (default 0).
func writeEngineScript(t *testing.T, path, logPath string) {
	t.Helper()
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + logPath + "\"\nexit ${SBX_EXIT:-0}\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// --- pure helpers -----------------------------------------------------------

func TestExitCode(t *testing.T) {
	requireExec(t, "sh")
	if c := exitCode(nil); c != 0 {
		t.Errorf("exitCode(nil) = %d, want 0", c)
	}
	err := exec.Command("sh", "-c", "exit 7").Run()
	if c := exitCode(err); c != 7 {
		t.Errorf("exitCode(exit 7) = %d, want 7", c)
	}
	startErr := exec.Command(filepath.Join(t.TempDir(), "does-not-exist")).Run()
	if c := exitCode(startErr); c != 1 {
		t.Errorf("exitCode(start failure) = %d, want 1", c)
	}
}

func TestIsTerminal(t *testing.T) {
	if IsTerminal(&bytes.Buffer{}) {
		t.Error("a *bytes.Buffer is not a terminal")
	}
	f, err := os.CreateTemp(t.TempDir(), "f")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if IsTerminal(f) {
		t.Error("a regular file is not a terminal")
	}
}

func TestPathExists(t *testing.T) {
	f := filepath.Join(t.TempDir(), "f")
	if pathExists(f) {
		t.Error("missing path reported as existing")
	}
	if err := os.WriteFile(f, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if !pathExists(f) {
		t.Error("existing path reported missing")
	}
}

func TestExtraMountsAndEnv(t *testing.T) {
	if extraMountsAndEnv(nil) != nil {
		t.Error("nil profile should yield nil")
	}
	p := &config.Profile{
		ExtraMounts: []config.Mount{
			{Source: "/s", Target: "/t"}, // default mode rw
			{Source: "/a", Target: "/b", Mode: "ro"},
		},
		Env: map[string]string{"K": "V"},
	}
	got := strings.Join(extraMountsAndEnv(p), " ")
	for _, want := range []string{"--volume /s:/t:rw", "--volume /a:/b:ro", "--env K=V"} {
		if !strings.Contains(got, want) {
			t.Errorf("extraMountsAndEnv missing %q in %q", want, got)
		}
	}
}

// TestNoCredentialPassthrough pins the auth posture: NOTHING credential-like
// leaves the host — no API-key env vars, no credential-dir mounts. The user
// logs in or exports keys INSIDE the sandbox (its private $HOME persists).
func TestNoCredentialPassthrough(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "secret")

	argv, err := RunArgv(RunOpts{MountDest: true, Engine: "docker", Image: "img:1", Dest: "/d", Slug: "s", Args: []string{"true"}})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(argv, " ")
	if strings.Contains(got, "ANTHROPIC_API_KEY") || strings.Contains(got, "secret") {
		t.Errorf("host API key leaked into the argv: %q", got)
	}
	if strings.Contains(got, ".claude") {
		t.Errorf("host credential dir leaked into the argv: %q", got)
	}
}

func TestDetectEngine(t *testing.T) {
	if e, err := DetectEngine(config.Defaults{Engine: "custom"}); err != nil || e != "custom" {
		t.Errorf("explicit engine = %q, %v; want custom", e, err)
	}

	bin := t.TempDir()
	t.Setenv("PATH", bin)
	if _, err := DetectEngine(config.Defaults{}); err == nil {
		t.Error("DetectEngine with no engines should error")
	}
	// docker only.
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if e, err := DetectEngine(config.Defaults{}); err != nil || e != "docker" {
		t.Errorf("docker-only = %q, %v; want docker", e, err)
	}
	// docker takes precedence over podman.
	if err := os.WriteFile(filepath.Join(bin, "podman"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if e, err := DetectEngine(config.Defaults{}); err != nil || e != "docker" {
		t.Errorf("podman+docker = %q, %v; want docker", e, err)
	}
}

func TestResolveEngine(t *testing.T) {
	// SANDBOXER_ENGINE wins regardless of the requested backend.
	if e, err := ResolveEngine("docker", config.Defaults{Engine: "custom"}); err != nil || e != "custom" {
		t.Errorf("explicit engine = %q, %v; want custom", e, err)
	}

	bin := t.TempDir()
	t.Setenv("PATH", bin)
	for _, name := range []string{"podman", "docker"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// An explicitly requested, installed engine is honored even though docker
	// would otherwise win the auto-detect — the --backend choice must pin it.
	if e, err := ResolveEngine("docker", config.Defaults{}); err != nil || e != "docker" {
		t.Errorf("backend=docker (both installed) = %q, %v; want docker", e, err)
	}
	if e, err := ResolveEngine("podman", config.Defaults{}); err != nil || e != "podman" {
		t.Errorf("backend=podman (both installed) = %q, %v; want podman", e, err)
	}

	// A requested-but-missing engine falls back to whatever is installed, so a
	// requested "podman" still works on a docker-only host.
	dockerOnly := t.TempDir()
	t.Setenv("PATH", dockerOnly)
	if err := os.WriteFile(filepath.Join(dockerOnly, "docker"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if e, err := ResolveEngine("podman", config.Defaults{}); err != nil || e != "docker" {
		t.Errorf("backend=podman on docker-only host = %q, %v; want docker fallback", e, err)
	}
	// EngineLabel never errors and reflects that fallback.
	if l := EngineLabel("podman", config.Defaults{}); l != "docker" {
		t.Errorf("EngineLabel(podman) on docker-only = %q; want docker", l)
	}
	// With no engine installed, EngineLabel returns the requested backend as-is.
	t.Setenv("PATH", t.TempDir())
	if l := EngineLabel("podman", config.Defaults{}); l != "podman" {
		t.Errorf("EngineLabel(podman) with no engine = %q; want podman", l)
	}
}

// --- container backend (fake engine) ---------------------------------------

func TestContainerRun(t *testing.T) {
	requireExec(t, "sh")
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	engine := filepath.Join(dir, "engine")
	writeEngineScript(t, engine, logPath)
	dest := t.TempDir()

	code, err := Run(RunOpts{
		MountDest: true, Engine: engine, Image: "toolbox:latest", Dest: dest, Slug: "s",
		RT: config.Runtime{}, NoEgress: true, Args: []string{"echo", "hi"},
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
	})
	if err != nil || code != 0 {
		t.Fatalf("Run = (%d,%v)", code, err)
	}
	log, _ := os.ReadFile(logPath)
	s := string(log)
	for _, want := range []string{
		"run --rm", "--cap-drop=ALL", "no-new-privileges",
		"--workdir " + dest, dest + ":" + dest + ":rw",
		"SANDBOXER_IN_CONTAINER=1", "SANDBOXER_SLUG=s",
		"toolbox:latest", "echo hi",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("Run args missing %q in %q", want, s)
		}
	}
	// A non-podman engine and a non-interactive run omit these.
	if strings.Contains(s, "--userns=keep-id") {
		t.Error("non-podman engine should not set --userns=keep-id")
	}
	if strings.Contains(s, " -i ") {
		t.Error("non-interactive run should not pass -i")
	}
}

func TestContainerRunProxyAndExit(t *testing.T) {
	requireExec(t, "sh")
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	engine := filepath.Join(dir, "engine")
	writeEngineScript(t, engine, logPath)
	t.Setenv("SBX_EXIT", "7")

	code, err := Run(RunOpts{
		MountDest: true, Engine: engine, Image: "img", Dest: t.TempDir(), Slug: "s",
		RT:       config.Runtime{Proxy: "http://p", Domains: []string{"x.com"}},
		NoEgress: true, Interactive: true, Args: []string{"true"},
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("Run err: %v", err)
	}
	if code != 7 {
		t.Errorf("exit code = %d, want 7 (propagated from engine)", code)
	}
	log, _ := os.ReadFile(logPath)
	s := string(log)
	for _, want := range []string{"HTTP_PROXY=http://p", "SANDBOXER_ALLOW_DOMAINS=x.com", " -i "} {
		if !strings.Contains(s, want) {
			t.Errorf("Run args missing %q in %q", want, s)
		}
	}
}

func TestContainerRunPodman(t *testing.T) {
	requireExec(t, "sh")
	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "calls.log")
	writeEngineScript(t, filepath.Join(bin, "podman"), logPath)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	code, err := Run(RunOpts{
		MountDest: true, Engine: "podman", Image: "img", Dest: t.TempDir(), Slug: "s",
		RT: config.Runtime{}, NoEgress: true, Args: []string{"true"},
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
	})
	if err != nil || code != 0 {
		t.Fatalf("Run podman = (%d,%v)", code, err)
	}
	log, _ := os.ReadFile(logPath)
	if !strings.Contains(string(log), "--userns=keep-id") {
		t.Errorf("podman engine should set --userns=keep-id:\n%s", log)
	}
}

func TestContainerRunLimits(t *testing.T) {
	requireExec(t, "sh")
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	engine := filepath.Join(dir, "engine")
	writeEngineScript(t, engine, logPath)

	code, err := Run(RunOpts{
		MountDest: true, Engine: engine, Image: "img", Dest: t.TempDir(), Slug: "s",
		RT: config.Runtime{}, NoEgress: true,
		Mem: "2G", CPU: "100%",
		Args:  []string{"bash", "-lc", "true"},
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
	})
	if err != nil || code != 0 {
		t.Fatalf("Run = (%d,%v)", code, err)
	}
	s, _ := os.ReadFile(logPath)
	for _, want := range []string{"--memory 2G", "--cpus 1"} {
		if !strings.Contains(string(s), want) {
			t.Errorf("limit flag %q missing from engine argv:\n%s", want, s)
		}
	}
}

func TestCPUsFromQuota(t *testing.T) {
	cases := map[string]string{"": "", "100%": "1", "50%": "0.5", "150%": "1.5", "2": "2", "bad%": ""}
	for in, want := range cases {
		if got := cpusFromQuota(in); got != want {
			t.Errorf("cpusFromQuota(%q) = %q, want %q", in, got, want)
		}
	}
}
