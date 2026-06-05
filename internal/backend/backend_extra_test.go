package backend

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/toolbox"
)

// TestContainerRunWithEgress exercises the egress-allowlist branch of Run: the
// fake engine succeeds for every network/run/connect call, so egress.Up brings
// the sidecar up and Run wires the proxy env and --network flags.
func TestContainerRunWithEgress(t *testing.T) {
	requireExec(t, "sh")
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	engine := filepath.Join(dir, "engine")
	writeEngineScript(t, engine, logPath) // exits 0 for all subcommands

	code, err := Run(RunOpts{
		Engine: engine, Image: "toolbox:latest", Dest: t.TempDir(), Slug: "s",
		RT:       config.Runtime{Egress: true, Domains: []string{"a.com"}},
		NoEgress: false, Args: []string{"true"},
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
	})
	if err != nil || code != 0 {
		t.Fatalf("Run with egress = (%d,%v)", code, err)
	}
	log, _ := os.ReadFile(logPath)
	s := string(log)
	// The sidecar was created and the agent run joined its network with proxy env.
	for _, want := range []string{"network create --internal", "--allow a.com", "--network ", "HTTP_PROXY=http://"} {
		if !strings.Contains(s, want) {
			t.Errorf("egress Run missing %q in:\n%s", want, s)
		}
	}
	// Teardown ran on the deferred Down().
	if !strings.Contains(s, "network rm") {
		t.Errorf("egress teardown not invoked:\n%s", s)
	}
}

// TestContainerRunEgressFailRefuses: when the allowlist is required but the
// sidecar cannot start, Run fails closed — it errors and never launches the
// agent on an open bridge network.
func TestContainerRunEgressFailRefuses(t *testing.T) {
	requireExec(t, "sh")
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	engine := filepath.Join(dir, "engine")
	writeEngineScript(t, engine, logPath)
	t.Setenv("SBX_EXIT", "1") // every engine call fails, including `network create`

	code, err := Run(RunOpts{
		Engine: engine, Image: "img", Dest: t.TempDir(), Slug: "s",
		RT:       config.Runtime{Egress: true, Domains: []string{"a.com"}},
		NoEgress: false, Args: []string{"true"},
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
	})
	if err == nil {
		t.Fatal("egress required but proxy failed should error (no bridge fallback)")
	}
	if code != 0 {
		t.Errorf("failed-egress exit code = %d, want 0 alongside the error", code)
	}
	log, _ := os.ReadFile(logPath)
	if strings.Contains(string(log), "run --rm") {
		t.Errorf("agent container ran despite egress failure:\n%s", log)
	}
}

// TestRunArgv exercises the pure run-argv builder across the resource-limit,
// upstream-proxy, egress-env and wall-timeout branches without a real engine.
func TestRunArgv(t *testing.T) {
	argv, err := RunArgv(RunOpts{
		Engine: "podman", Image: "img:1", Dest: "/d", Slug: "s",
		RT: config.Runtime{
			HTTPProxy: "http://p", HTTPSProxy: "http://p", NoProxy: "x",
			Domains: []string{"a.com"}, Egress: true,
		},
		Mem: "2G", CPU: "150%", Wall: "60", Interactive: true,
		Args: []string{"bash", "-l"},
	})
	if err != nil {
		t.Fatalf("RunArgv: %v", err)
	}
	s := strings.Join(argv, " ")
	for _, w := range []string{
		"run --rm", "--user", "--memory 2G", "--cpus 1.5", "--userns=keep-id",
		"HTTP_PROXY=http://p", "NO_PROXY=x", "SANDBOXER_ALLOW_DOMAINS=a.com",
		"img:1", "timeout --signal=TERM 60", "bash -l",
	} {
		if !strings.Contains(s, w) {
			t.Errorf("RunArgv missing %q in:\n%s", w, s)
		}
	}
}

// TestEnsureImage covers the preflight: present image is a no-op; a missing
// custom image is left to the engine; a missing bundled default is auto-built
// (or fails fast with a build hint when auto-build is disabled).
func TestEnsureImage(t *testing.T) {
	// Override the test seams; restore after.
	origExists, origBuild := imageExists, buildImage
	defer func() { imageExists, buildImage = origExists, origBuild }()

	// 1. Present image → no build, no error.
	imageExists = func(string, string) bool { return true }
	built := false
	buildImage = func(toolbox.BuildOpts) error { built = true; return nil }
	if err := ensureImage(RunOpts{Engine: "e", Image: config.DefaultImage}); err != nil || built {
		t.Errorf("present image: err=%v built=%v; want nil/false", err, built)
	}

	// 2. Missing CUSTOM image → nil (engine pulls), no build.
	imageExists = func(string, string) bool { return false }
	built = false
	if err := ensureImage(RunOpts{Engine: "e", Image: "ghcr.io/x/y:1"}); err != nil || built {
		t.Errorf("custom missing image: err=%v built=%v; want nil/false", err, built)
	}

	// 3. Missing default + auto-build disabled → fail fast with the hint.
	t.Setenv("SANDBOXER_NO_AUTOBUILD", "1")
	built = false
	err := ensureImage(RunOpts{Engine: "e", Image: config.DefaultImage})
	if err == nil || !strings.Contains(err.Error(), "sandboxer build-image") {
		t.Errorf("autobuild-disabled: err=%v; want a 'sandboxer build-image' hint", err)
	}
	if built {
		t.Error("autobuild-disabled should not build")
	}

	// 4. Missing default + auto-build → builds, then present.
	t.Setenv("SANDBOXER_NO_AUTOBUILD", "")
	calls := 0
	imageExists = func(string, string) bool { calls++; return calls > 1 } // missing then present
	gotOpts := toolbox.BuildOpts{}
	buildImage = func(o toolbox.BuildOpts) error { gotOpts = o; return nil }
	if err := ensureImage(RunOpts{Engine: "podman", Image: config.DefaultImage, Stderr: &bytes.Buffer{}}); err != nil {
		t.Errorf("auto-build: unexpected err %v", err)
	}
	if gotOpts.Engine != "podman" || gotOpts.Image != config.DefaultImage {
		t.Errorf("auto-build passed wrong opts: %+v", gotOpts)
	}

	// 5. Build "succeeds" but image still absent → error.
	imageExists = func(string, string) bool { return false }
	buildImage = func(toolbox.BuildOpts) error { return nil }
	if err := ensureImage(RunOpts{Engine: "e", Image: config.DefaultImage, Stderr: &bytes.Buffer{}}); err == nil {
		t.Error("still-missing after build should error")
	}
}

// TestContainerRunEgressNoDomains: egress on with an empty allowlist is a
// misconfiguration, not an open-network run.
func TestContainerRunEgressNoDomains(t *testing.T) {
	code, err := Run(RunOpts{
		Engine: "true", Image: "img", Dest: t.TempDir(), Slug: "s",
		RT:       config.Runtime{Egress: true}, // no domains
		NoEgress: false, Args: []string{"true"},
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
	})
	if err == nil || code != 0 {
		t.Fatalf("egress on with no domains = (%d,%v), want (0, error)", code, err)
	}
}
