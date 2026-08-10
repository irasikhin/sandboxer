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

// TestIsInteractiveTerminalRejectsDevNull is the reason the stricter check
// exists. /dev/null IS a character device, so IsTerminal answers yes for it —
// harmless when choosing the runner's -t flag, and not harmless for a blocking
// prompt: `sandboxer enter < /dev/null` printed a question into the void.
// Observed on a real engine before this split.
func TestIsInteractiveTerminalRejectsDevNull(t *testing.T) {
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Skipf("no %s: %v", os.DevNull, err)
	}
	defer devnull.Close()
	if !IsTerminal(devnull) {
		t.Skip("this platform does not report /dev/null as a character device")
	}
	if IsInteractiveTerminal(devnull) {
		t.Error("/dev/null must never read as a terminal a user can answer on")
	}
	if IsInteractiveTerminal(&bytes.Buffer{}) {
		t.Error("a *bytes.Buffer is not a terminal")
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

// TestNoCredentialPassthrough pins the auth posture: NOTHING credential-like
// leaves the host implicitly — no ambient API-key env vars, no credential-dir
// mounts. Credentials travel only through the explicit AuthEnv channel (and on
// the microVM runners only as key REFERENCES in argv, never values).
func TestNoCredentialPassthrough(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "secret")

	for name, argv := range map[string][]string{
		"smolvm run":       vmRunArgv(RunOpts{MountDest: true, Engine: smolvmEngine, Image: "img:1", Dest: "/d", Slug: "s", Args: []string{"true"}}),
		"microsandbox run": msbRunArgv(RunOpts{MountDest: true, Engine: msbEngine, Image: "img:1", Dest: "/d", Slug: "s", Args: []string{"true"}}),
	} {
		got := strings.Join(argv, " ")
		if strings.Contains(got, "ANTHROPIC_API_KEY") || strings.Contains(got, "secret") {
			t.Errorf("%s: host API key leaked into the argv: %q", name, got)
		}
		if strings.Contains(got, ".claude") {
			t.Errorf("%s: host credential dir leaked into the argv: %q", name, got)
		}
	}

	// Even an explicit AuthEnv entry never puts the VALUE into smolvm's argv —
	// only the key reference (--secret-env KEY=KEY).
	argv := vmRunArgv(RunOpts{Engine: smolvmEngine, Image: "img:1", Dest: "/d", Slug: "s",
		AuthEnv: []string{"ANTHROPIC_API_KEY=sk-tokenvalue"}, Args: []string{"true"}})
	got := strings.Join(argv, " ")
	if strings.Contains(got, "sk-tokenvalue") {
		t.Errorf("auth VALUE leaked into smolvm argv: %q", got)
	}
	if !strings.Contains(got, "--secret-env ANTHROPIC_API_KEY=ANTHROPIC_API_KEY") {
		t.Errorf("auth key reference missing from smolvm argv: %q", got)
	}
}

// TestResolveEngine: only the microVM backends resolve, each to its runner's
// engine identity; the removed container-era names and anything unknown error
// with the migration hint.
func TestResolveEngine(t *testing.T) {
	// given a host where both runner binaries exist
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	for _, name := range []string{"smolvm", "msb"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("SANDBOXER_SMOLVM", "")
	t.Setenv("SANDBOXER_MSB", "")

	// when/then: the two microVM backends resolve to their engine identities
	if e, err := ResolveEngine("microvm", config.Defaults{}); err != nil || e != smolvmEngine {
		t.Errorf("ResolveEngine(microvm) = %q, %v; want %q", e, err, smolvmEngine)
	}
	if e, err := ResolveEngine("microsandbox", config.Defaults{}); err != nil || e != msbEngine {
		t.Errorf("ResolveEngine(microsandbox) = %q, %v; want %q", e, err, msbEngine)
	}

	// then: container-era and unknown names error, naming the removal
	for _, be := range []string{"", "auto", "docker", "podman", "native", "bogus"} {
		e, err := ResolveEngine(be, config.Defaults{})
		if err == nil {
			t.Errorf("ResolveEngine(%q) = %q, want error", be, e)
			continue
		}
		if !strings.Contains(err.Error(), "container backend was removed") {
			t.Errorf("ResolveEngine(%q) error %q must name the container-backend removal", be, err)
		}
	}

	// then: a requested runner whose binary is absent errors with the install hint
	t.Setenv("PATH", t.TempDir())
	if _, err := ResolveEngine("microvm", config.Defaults{}); err == nil ||
		!strings.Contains(err.Error(), "smolvm") {
		t.Errorf("ResolveEngine(microvm) without smolvm = %v, want install hint", err)
	}
	if _, err := ResolveEngine("microsandbox", config.Defaults{}); err == nil ||
		!strings.Contains(err.Error(), "msb") {
		t.Errorf("ResolveEngine(microsandbox) without msb = %v, want install hint", err)
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
