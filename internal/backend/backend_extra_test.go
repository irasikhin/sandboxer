package backend

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
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

// TestWarnIfImageMissing: a present image is silent; a missing one prints the
// build hint; degenerate inputs are no-ops.
func TestWarnIfImageMissing(t *testing.T) {
	requireExec(t, "true", "false")
	var buf bytes.Buffer
	// `true` ignores args and exits 0 → image treated as present → no warning.
	warnIfImageMissing("true", "img", &buf)
	if buf.Len() != 0 {
		t.Errorf("present image should not warn, got %q", buf.String())
	}
	// `false` exits non-zero → image treated as missing → actionable hint.
	buf.Reset()
	warnIfImageMissing("false", "sandboxer-toolbox:latest", &buf)
	if !strings.Contains(buf.String(), "nix run .#build-image") {
		t.Errorf("missing image should print build hint, got %q", buf.String())
	}
	// nil writer / empty fields must not panic and must stay silent.
	warnIfImageMissing("", "", nil)
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
