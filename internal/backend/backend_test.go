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
	if isTerminal(&bytes.Buffer{}) {
		t.Error("a *bytes.Buffer is not a terminal")
	}
	f, err := os.CreateTemp(t.TempDir(), "f")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTerminal(f) {
		t.Error("a regular file is not a terminal")
	}
}

func TestProxyEnv(t *testing.T) {
	rt := config.Runtime{HTTPProxy: "http://p", HTTPSProxy: "https://p", NoProxy: "localhost"}
	got := strings.Join(ProxyEnv(rt), " ")
	for _, want := range []string{
		"HTTP_PROXY=http://p", "http_proxy=http://p",
		"HTTPS_PROXY=https://p", "https_proxy=https://p",
		"NO_PROXY=localhost", "no_proxy=localhost",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ProxyEnv missing %q in %q", want, got)
		}
	}
	if env := ProxyEnv(config.Runtime{}); env != nil {
		t.Errorf("empty runtime ProxyEnv = %v, want nil", env)
	}
}

func TestExpandHome(t *testing.T) {
	cases := []struct{ in, home, want string }{
		{"~", "/home/u", "/home/u"},
		{"~/x", "/home/u", "/home/u/x"},
		{"/abs", "/home/u", "/abs"},
		{"rel/path", "/home/u", "rel/path"},
	}
	for _, c := range cases {
		if got := expandHome(c.in, c.home); got != c.want {
			t.Errorf("expandHome(%q,%q) = %q, want %q", c.in, c.home, got, c.want)
		}
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

func TestOriginMounts(t *testing.T) {
	d1, d2 := t.TempDir(), t.TempDir()
	manifest := filepath.Join(t.TempDir(), "manifest.json")
	body := `[{"origin":"` + d1 + `","mode":"rw"},` +
		`{"origin":"` + d2 + `","mode":"ro"},` +
		`{"origin":"","mode":"rw"},` +
		`{"origin":"/no/such/path","mode":"rw"}]`
	if err := os.WriteFile(manifest, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := originMounts(manifest)
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, d1+":"+d1+":rw") {
		t.Errorf("missing rw mount for %s: %q", d1, joined)
	}
	if !strings.Contains(joined, d2+":"+d2+":ro") {
		t.Errorf("missing ro mount for %s: %q", d2, joined)
	}
	if len(got) != 4 { // two --volume/value pairs, empty + missing skipped
		t.Errorf("originMounts produced %d args, want 4: %v", len(got), got)
	}
	if originMounts(filepath.Join(t.TempDir(), "missing")) != nil {
		t.Error("missing manifest should yield nil")
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	_ = os.WriteFile(bad, []byte("not json"), 0o644)
	if originMounts(bad) != nil {
		t.Error("malformed manifest should yield nil")
	}
}

func TestAuthFlags(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "secret")
	// Ensure the optional/other dirs are absent so they're skipped.
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")

	af, err := authFlags([]string{"claude"}, false, "")
	if err != nil {
		t.Fatalf("authFlags: %v", err)
	}
	got := strings.Join(af, " ")
	wantVol := filepath.Join(home, ".claude") + ":" + filepath.Join(home, ".claude") + ":rw"
	if !strings.Contains(got, wantVol) {
		t.Errorf("authFlags missing creds volume %q in %q", wantVol, got)
	}
	if !strings.Contains(got, "ANTHROPIC_API_KEY=secret") {
		t.Errorf("authFlags missing auth env in %q", got)
	}
	// The optional, non-existent ~/.config/anthropic must not appear.
	if strings.Contains(got, ".config/anthropic") {
		t.Errorf("non-existent optional dir leaked: %q", got)
	}
	// An unknown agent is skipped silently.
	if a, err := authFlags([]string{"nope"}, false, ""); err != nil || a != nil {
		t.Errorf("unknown agent should yield nil, got %v (err %v)", a, err)
	}
}

func TestAuthFlagsEphemeral(t *testing.T) {
	requireExec(t, "cp")
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", "creds"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ephDir := filepath.Join(t.TempDir(), "eph")

	af, err := authFlags([]string{"claude"}, true, ephDir)
	if err != nil {
		t.Fatalf("authFlags ephemeral: %v", err)
	}
	got := strings.Join(af, " ")
	ephClaude := filepath.Join(ephDir, ".claude")
	if !strings.Contains(got, ephClaude+":"+ephClaude+":rw") {
		t.Errorf("ephemeral mount not pointed at copy: %q", got)
	}
	if _, err := os.Stat(filepath.Join(ephClaude, "creds")); err != nil {
		t.Errorf("creds not copied into ephemeral dir: %v", err)
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
	// podman takes precedence over docker.
	if err := os.WriteFile(filepath.Join(bin, "podman"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if e, err := DetectEngine(config.Defaults{}); err != nil || e != "podman" {
		t.Errorf("podman+docker = %q, %v; want podman", e, err)
	}
}

// --- native backend ---------------------------------------------------------

func TestNativeExecGeneric(t *testing.T) {
	requireExec(t, "sh")
	dest := t.TempDir()
	rt := config.Runtime{HTTPProxy: "http://p"}

	if code, err := NativeExec(dest, rt, nil, nil, nil, nil); code != 2 || err == nil {
		t.Errorf("empty args = (%d,%v), want (2, error)", code, err)
	}

	var out bytes.Buffer
	code, err := NativeExec(dest, rt, []string{"sh", "-c", "exit 0"}, strings.NewReader(""), &out, &out)
	if code != 0 || err != nil {
		t.Errorf("exit 0 cmd = (%d,%v)", code, err)
	}
	code, _ = NativeExec(dest, rt, []string{"sh", "-c", "exit 3"}, strings.NewReader(""), &out, &out)
	if code != 3 {
		t.Errorf("exit 3 cmd = %d, want 3", code)
	}

	// Runs in dest with the proxy env injected.
	_, err = NativeExec(dest, rt, []string{"sh", "-c", `echo "$PWD $HTTP_PROXY" > marker`}, strings.NewReader(""), &out, &out)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dest, "marker"))
	if !strings.Contains(string(data), dest) || !strings.Contains(string(data), "http://p") {
		t.Errorf("command did not run in dest with proxy env: %q", data)
	}
}

func TestNativeExecClaudeWrapping(t *testing.T) {
	requireExec(t, "sh")
	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "claude.log")
	writeEngineScript(t, filepath.Join(bin, "claude"), logPath)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	rt := config.Runtime{Model: "sonnet", Domains: []string{"api.anthropic.com"}}
	code, err := NativeExec(t.TempDir(), rt, []string{"claude", "hello"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil || code != 0 {
		t.Fatalf("NativeExec claude = (%d,%v)", code, err)
	}
	log, _ := os.ReadFile(logPath)
	s := string(log)
	for _, want := range []string{"--settings", "allowedDomains", "api.anthropic.com", "--model sonnet", "hello"} {
		if !strings.Contains(s, want) {
			t.Errorf("claude invocation missing %q in %q", want, s)
		}
	}
}

func TestNativeEnter(t *testing.T) {
	requireExec(t, "sh")
	dest := t.TempDir()
	marker := filepath.Join(t.TempDir(), "shell.log")
	shell := filepath.Join(t.TempDir(), "shell")
	script := "#!/bin/sh\nprintf '%s\\n%s\\n' \"$PWD\" \"$HTTP_PROXY\" > \"" + marker + "\"\n"
	if err := os.WriteFile(shell, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", shell)

	if err := NativeEnter(dest, config.Runtime{HTTPProxy: "http://p"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("NativeEnter: %v", err)
	}
	data, _ := os.ReadFile(marker)
	if !strings.Contains(string(data), dest) || !strings.Contains(string(data), "http://p") {
		t.Errorf("shell did not run in dest with proxy env: %q", data)
	}

	// A failing shell surfaces an error.
	t.Setenv("SHELL", "false")
	if err := NativeEnter(dest, config.Runtime{}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Error("a non-zero shell exit should be returned as an error")
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
		Engine: engine, Image: "toolbox:latest", Dest: dest, Slug: "s",
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
		Engine: engine, Image: "img", Dest: t.TempDir(), Slug: "s",
		RT:       config.Runtime{HTTPProxy: "http://p", Domains: []string{"x.com"}},
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
		Engine: "podman", Image: "img", Dest: t.TempDir(), Slug: "s",
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
		Engine: engine, Image: "img", Dest: t.TempDir(), Slug: "s",
		RT: config.Runtime{}, NoEgress: true,
		Mem: "2G", CPU: "100%", Wall: "30",
		Args:  []string{"bash", "-lc", "true"},
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
	})
	if err != nil || code != 0 {
		t.Fatalf("Run = (%d,%v)", code, err)
	}
	s, _ := os.ReadFile(logPath)
	for _, want := range []string{"--memory 2G", "--cpus 1", "timeout --signal=TERM 30"} {
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
