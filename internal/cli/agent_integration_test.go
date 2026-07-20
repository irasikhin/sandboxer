//go:build integration

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/itest"
)

// TestExec_Container_AgentEnvAndHomeIsolation locks in the agent-facing
// contract sandboxer sets up around a coding agent, end to end through the real
// CLI: the sandbox identity env is present, an agent's auth env var
// (ANTHROPIC_API_KEY) is passed through, and — the security invariant — the
// host's real home is NEVER mounted. Egress is off, so this needs only the
// toolbox image. The agent itself is not run; the point is the environment
// sandboxer hands it, which a stub agent would inherit identically.
//
// Both halves hang off `hostConfigs = true`, which is what the profile must opt
// into for host auth to travel at all (hostAuthEnv). The probe file is
// deliberately NOT one the registry seeds: with the opt-in on, a host
// ~/.claude.json arriving in the sandbox home is SeedHome doing its job, not a
// leak — so probing for it tested the wrong thing. The invariant here is that
// the host home is not a mount, and only a file nothing seeds can show that.
func TestExec_Container_AgentEnvAndHomeIsolation(t *testing.T) {
	engine := itest.Engine(t)
	image := itest.EnsureToolboxImage(t, engine)
	t.Setenv("SANDBOXER_ENGINE", engine)
	t.Setenv("SANDBOXER_IMAGE", image)
	t.Setenv("SANDBOXER_NO_EGRESS", "1") // this test is about agent env + home, not the proxy
	t.Setenv("SANDBOXER_NO_AUTOBUILD", "1")
	t.Setenv("ANTHROPIC_API_KEY", "sbx-itest-key")

	// A host home carrying a file no agent's seed list names, so its presence
	// inside could only mean the host home itself was mounted.
	hostHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(hostHome, ".host-only-marker"), []byte("HOST-SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}

	project := newProject(t)
	t.Setenv("HOME", hostHome)
	cfg := filepath.Join(t.TempDir(), "sbx.nix")
	if err := os.WriteFile(cfg, []byte("{ name = \"feat\"; backend = \""+engine+"\"; hostConfigs = true; srcs = [ { src = \".\"; branch = \"feat/x\"; } ]; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if code, out, errs := run("create", "--src", project, "--config", cfg); code != 0 {
		t.Fatalf("create = %d\nout: %s\nerr: %s", code, out, errs)
	}

	probe := "echo INC=$SANDBOXER_IN_CONTAINER SLUG=$SANDBOXER_SLUG; " +
		"echo KEY=$ANTHROPIC_API_KEY; " +
		`if [ -f "$HOME/.host-only-marker" ]; then echo HOST_HOME_MOUNTED; else echo HOME_ISOLATED; fi`

	code, out, errs := run("exec", "feat", "--src", project, "--config", cfg,
		"--ephemeral", "--", "bash", "-c", probe)
	if code != 0 {
		t.Fatalf("exec = %d\nout: %s\nerr: %s", code, out, errs)
	}
	if !strings.Contains(out, "INC=1") || !strings.Contains(out, "SLUG=feat") {
		t.Errorf("sandbox identity env missing:\nout: %s", out)
	}
	if !strings.Contains(out, "KEY=sbx-itest-key") {
		t.Errorf("agent auth env ANTHROPIC_API_KEY was not passed through:\nout: %s", out)
	}
	if strings.Contains(out, "HOST_HOME_MOUNTED") || !strings.Contains(out, "HOME_ISOLATED") {
		t.Errorf("host home leaked into the sandbox — isolation broken:\nout: %s", out)
	}
}
