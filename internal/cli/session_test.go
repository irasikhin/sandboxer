package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/backend"
	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/registry"
)

// sessionCalls records what the stubbed backend seams were asked to do.
type sessionCalls struct {
	ensure  []backend.RunOpts
	exec    [][]string // cmdArgs per ExecSession call
	execTo  []string   // container name per ExecSession call
	run     []backend.RunOpts
	inspect []string          // names inspected
	hash    []backend.RunOpts // opts per SessionWantHash call
	imageID []string          // images whose ID was queried
}

// stubSessionSeams replaces the persistent-session seams (and backendRun) with
// recorders, restoring them when the test ends. inspect returns info for every
// container; the want-hash seam returns wantHash so a test can force a
// fresh/stale verdict without computing real hashes; the image-ID seam returns
// "" (image unknown — the freshness check is skipped) unless a test overrides
// it. SANDBOXER_SESSION is cleared so the mode under test comes only from
// flags/profile (tests that exercise the env set it themselves afterwards).
func stubSessionSeams(t *testing.T, info backend.SessionInfo, wantHash string) *sessionCalls {
	t.Helper()
	t.Setenv("SANDBOXER_SESSION", "")
	c := &sessionCalls{}
	oldEnsure, oldExec, oldRun := backendEnsureSession, backendExecSession, backendRun
	oldInspect, oldHash, oldImageID := backendInspectSession, backendWantHash, backendImageID
	t.Cleanup(func() {
		backendEnsureSession, backendExecSession, backendRun = oldEnsure, oldExec, oldRun
		backendInspectSession, backendWantHash, backendImageID = oldInspect, oldHash, oldImageID
	})
	backendEnsureSession = func(o backend.RunOpts) (string, error) {
		c.ensure = append(c.ensure, o)
		return backend.SessionName(o.Slug, o.BaseDir), nil
	}
	backendExecSession = func(o backend.RunOpts, name string, cmdArgs []string) (int, error) {
		c.execTo = append(c.execTo, name)
		c.exec = append(c.exec, cmdArgs)
		return 0, nil
	}
	backendRun = func(o backend.RunOpts) (int, error) {
		c.run = append(c.run, o)
		return 0, nil
	}
	backendInspectSession = func(engine, name string) backend.SessionInfo {
		c.inspect = append(c.inspect, name)
		return info
	}
	backendWantHash = func(o backend.RunOpts) string {
		c.hash = append(c.hash, o)
		return wantHash
	}
	backendImageID = func(engine, image string) string {
		c.imageID = append(c.imageID, image)
		return ""
	}
	return c
}

// sessionProject creates a project with one sandbox "feat" and a fake engine,
// ready for enter/exec routing tests.
func sessionProject(t *testing.T) string {
	t.Helper()
	project := newProject(t)
	fakePodman(t)
	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}
	return project
}

// TestEnterPersistentByDefault: a plain enter converges the session container
// (Ensure) and attaches via the tmux launcher (Exec) — backend.Run stays out
// of it. The banner names the container and the detach semantics.
func TestEnterPersistentByDefault(t *testing.T) {
	project := sessionProject(t)
	c := stubSessionSeams(t, backend.SessionInfo{}, "h")

	code, _, errs := run("enter", "feat", "--src", project)
	if code != 0 {
		t.Fatalf("enter = %d, %s", code, errs)
	}
	if len(c.ensure) != 1 || len(c.exec) != 1 || len(c.run) != 0 {
		t.Fatalf("calls: ensure=%d exec=%d run=%d, want 1/1/0", len(c.ensure), len(c.exec), len(c.run))
	}
	o := c.ensure[0]
	wantBase := config.StateDir(project)
	if o.Slug != "feat" || o.BaseDir != wantBase {
		t.Errorf("ensure opts slug=%q base=%q, want feat/%s", o.Slug, o.BaseDir, wantBase)
	}
	name := backend.SessionName("feat", wantBase)
	if c.execTo[0] != name {
		t.Errorf("exec target = %q, want %q", c.execTo[0], name)
	}
	argv := c.exec[0]
	if len(argv) != 3 || argv[0] != "bash" || argv[1] != "-c" || !strings.Contains(argv[2], "bash") {
		t.Errorf("exec argv = %v", argv)
	}
	for _, want := range []string{
		name, "keeps the container running", "sandboxer enter feat", "sandboxer: done in",
		"sandboxer: src ", "feat/feat", // the connected-repos lines: repo → branch (path)
	} {
		if !strings.Contains(errs, want) {
			t.Errorf("stderr missing %q:\n%s", want, errs)
		}
	}
}

// TestEnterPassesHostAuthEnv: with hostConfigs on (the scaffold default), the
// registry agents' auth env vars set on the HOST ride into the container opts
// — sorted, deduped, empties dropped — so a long-lived `claude setup-token`
// authenticates every sandbox without copying a rotating credentials file.
func TestEnterPassesHostAuthEnv(t *testing.T) {
	project := sessionProject(t)
	c := stubSessionSeams(t, backend.SessionInfo{}, "h")
	clearAuthEnv(t)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok-123")

	if code, _, errs := run("enter", "feat", "--src", project); code != 0 {
		t.Fatalf("enter = %d, %s", code, errs)
	}
	got := c.ensure[0].AuthEnv
	if len(got) != 1 || got[0] != "CLAUDE_CODE_OAUTH_TOKEN=tok-123" {
		t.Errorf("AuthEnv = %v, want exactly the one set host var", got)
	}
}

// clearAuthEnv empties every registry auth var so a developer's own exported
// keys can never leak into a test's expectations.
func clearAuthEnv(t *testing.T) {
	t.Helper()
	for _, name := range registry.Names() {
		a, err := registry.Get(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range a.AuthEnv {
			t.Setenv(k, "")
		}
	}
}

// TestHostAuthEnvGate: no profile opt-in, no passthrough — hostConfigs owns
// both halves of host auth (config seed and env), and the helper sorts and
// dedupes across agents sharing a var.
func TestHostAuthEnvGate(t *testing.T) {
	clearAuthEnv(t)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "tok")
	t.Setenv("OPENAI_API_KEY", "oai") // listed by several agents — must appear once
	if got := hostAuthEnv(nil); got != nil {
		t.Errorf("nil profile: AuthEnv = %v, want none", got)
	}
	if got := hostAuthEnv(&config.Profile{}); got != nil {
		t.Errorf("hostConfigs off: AuthEnv = %v, want none", got)
	}
	got := hostAuthEnv(&config.Profile{HostConfigs: true})
	want := []string{"CLAUDE_CODE_OAUTH_TOKEN=tok", "OPENAI_API_KEY=oai"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("AuthEnv = %v, want %v (sorted, deduped)", got, want)
	}
}

// TestEnterRunsPlainShell: a persistent enter execs the plain interactive
// shell launcher — sandboxer starts no multiplexer (tmux/zellij are opt-in
// tools inside the image).
func TestEnterRunsPlainShell(t *testing.T) {
	project := sessionProject(t)
	c := stubSessionSeams(t, backend.SessionInfo{}, "h")

	if code, _, errs := run("enter", "feat", "--src", project); code != 0 {
		t.Fatalf("enter = %d, %s", code, errs)
	}
	if len(c.exec) != 1 || strings.Contains(c.exec[0][2], "tmux") ||
		!strings.Contains(c.exec[0][2], "bash") {
		t.Errorf("exec argv = %v, want the plain rc shell launcher", c.exec)
	}
}

// TestEnterEphemeralRouting: each of the three ephemeral switches (flag, env,
// profile) must force the one-shot backend.Run path.
func TestEnterEphemeralRouting(t *testing.T) {
	t.Run("flag", func(t *testing.T) {
		project := sessionProject(t)
		c := stubSessionSeams(t, backend.SessionInfo{}, "h")
		if code, _, errs := run("enter", "feat", "--src", project, "--ephemeral"); code != 0 {
			t.Fatalf("enter = %d, %s", code, errs)
		}
		if len(c.run) != 1 || len(c.ensure) != 0 || len(c.exec) != 0 {
			t.Errorf("calls: run=%d ensure=%d exec=%d, want 1/0/0", len(c.run), len(c.ensure), len(c.exec))
		}
	})
	t.Run("env", func(t *testing.T) {
		project := sessionProject(t)
		c := stubSessionSeams(t, backend.SessionInfo{}, "h")
		t.Setenv("SANDBOXER_SESSION", "ephemeral")
		if code, _, errs := run("enter", "feat", "--src", project); code != 0 {
			t.Fatalf("enter = %d, %s", code, errs)
		}
		if len(c.run) != 1 || len(c.ensure) != 0 {
			t.Errorf("calls: run=%d ensure=%d, want 1/0", len(c.run), len(c.ensure))
		}
	})
	t.Run("profile", func(t *testing.T) {
		project := newProject(t)
		fakePodman(t)
		c := stubSessionSeams(t, backend.SessionInfo{}, "h")
		cfg := filepath.Join(t.TempDir(), "p.nix")
		if err := os.WriteFile(cfg, []byte("{ name = \"feat\"; session = \"ephemeral\"; srcs = [ { src = \".\"; branch = \"feat/x\"; } ]; }\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if code, _, errs := run("create", "--src", project, "--config", cfg); code != 0 {
			t.Fatalf("create: %d %s", code, errs)
		}
		if code, _, errs := run("enter", "--src", project, "--config", cfg); code != 0 {
			t.Fatalf("enter = %d, %s", code, errs)
		}
		if len(c.run) != 1 || len(c.ensure) != 0 {
			t.Errorf("calls: run=%d ensure=%d, want 1/0", len(c.run), len(c.ensure))
		}
	})
}

// TestEnterUnknownSessionMode: a typo in SANDBOXER_SESSION fails fast.
func TestEnterUnknownSessionMode(t *testing.T) {
	project := sessionProject(t)
	c := stubSessionSeams(t, backend.SessionInfo{}, "h")
	t.Setenv("SANDBOXER_SESSION", "bogus")

	code, _, errs := run("enter", "feat", "--src", project)
	if code != 1 || !strings.Contains(errs, "unknown session mode") {
		t.Errorf("bogus mode = (%d, %q)", code, errs)
	}
	if len(c.ensure)+len(c.exec)+len(c.run) != 0 {
		t.Error("no backend call may happen on an unknown session mode")
	}
}

// TestEnterEnsureFailure: an EnsureSession error (busy session, egress down)
// is printed, deps are still pushed, and the command exits 1.
func TestEnterEnsureFailure(t *testing.T) {
	project := sessionProject(t)
	c := stubSessionSeams(t, backend.SessionInfo{}, "h")
	backendEnsureSession = func(o backend.RunOpts) (string, error) {
		return "", errors.New("other clients are attached")
	}

	code, _, errs := run("enter", "feat", "--src", project)
	if code != 1 {
		t.Fatalf("enter with failing ensure = %d, want 1", code)
	}
	if !strings.Contains(errs, "other clients are attached") {
		t.Errorf("ensure error not surfaced:\n%s", errs)
	}
	if !strings.Contains(errs, "sandboxer: done in") {
		t.Errorf("post-run dep sync must still happen:\n%s", errs)
	}
	if len(c.exec)+len(c.run) != 0 {
		t.Error("nothing may attach after a failed ensure")
	}
}

// TestEnterRecreatedDirForcesConverge: when enter had to (re)create the
// sandbox dir (`rm -rf ./sandboxes` by hand), a running-but-stale session is
// NOT protected into the one-shot fallback — its bind mounts hold the deleted
// tree, so enter converges it via EnsureSession (which recreates on the
// generation-flipped hash).
func TestEnterRecreatedDirForcesConverge(t *testing.T) {
	project := sessionProject(t)
	if err := os.RemoveAll(filepath.Join(project, "sandboxes")); err != nil {
		t.Fatal(err)
	}
	c := stubSessionSeams(t, backend.SessionInfo{Exists: true, Running: true, Hash: "old"}, "new")

	code, _, errs := run("enter", "feat", "--src", project)
	if code != 0 {
		t.Fatalf("enter = %d, %s", code, errs)
	}
	if !strings.Contains(errs, "does not exist — creating") {
		t.Errorf("enter did not recreate the hand-deleted sandbox:\n%s", errs)
	}
	if len(c.ensure) != 1 || len(c.exec) != 1 || len(c.run) != 0 {
		t.Fatalf("calls: ensure=%d exec=%d run=%d, want 1/1/0 (converge, not one-shot)",
			len(c.ensure), len(c.exec), len(c.run))
	}
	if g := c.ensure[0].DestGen; g == "" {
		t.Error("EnsureSession opts carry no DestGen — a recreated dir must flip the session hash")
	}
}

// TestExecRoutesToFreshSession: a running session whose recorded hash matches
// the wanted one serves the command via ExecSession.
func TestExecRoutesToFreshSession(t *testing.T) {
	project := sessionProject(t)
	c := stubSessionSeams(t, backend.SessionInfo{Exists: true, Running: true, Hash: "h"}, "h")

	code, _, errs := run("exec", "feat", "--src", project, "--", "echo", "hi")
	if code != 0 {
		t.Fatalf("exec = %d, %s", code, errs)
	}
	if len(c.inspect) != 1 || len(c.exec) != 1 || len(c.run) != 0 || len(c.ensure) != 0 {
		t.Fatalf("calls: inspect=%d exec=%d run=%d ensure=%d, want 1/1/0/0",
			len(c.inspect), len(c.exec), len(c.run), len(c.ensure))
	}
	if got := c.exec[0]; len(got) != 2 || got[0] != "echo" || got[1] != "hi" {
		t.Errorf("exec argv = %v", got)
	}
}

// TestExecStaleSessionFallsBack: a running but stale session is left alone —
// the command runs one-shot with a notice, and exec never recreates the
// daemon container (no Ensure call).
func TestExecStaleSessionFallsBack(t *testing.T) {
	project := sessionProject(t)
	c := stubSessionSeams(t, backend.SessionInfo{Exists: true, Running: true, Hash: "h"}, "new")

	code, _, errs := run("exec", "feat", "--src", project, "--", "true")
	if code != 0 {
		t.Fatalf("exec = %d, %s", code, errs)
	}
	if len(c.run) != 1 || len(c.exec) != 0 || len(c.ensure) != 0 {
		t.Errorf("calls: run=%d exec=%d ensure=%d, want 1/0/0", len(c.run), len(c.exec), len(c.ensure))
	}
	if !strings.Contains(errs, "stale") {
		t.Errorf("missing stale notice:\n%s", errs)
	}
}

// TestExecImageStaleFallsBack: a running session whose hash still matches but
// whose container runs an older build of the image (the engine now holds a
// different ID — rebuilt under the same tag) is left alone: one-shot run with
// an "image rebuilt" notice, never a session exec or recreate.
func TestExecImageStaleFallsBack(t *testing.T) {
	project := sessionProject(t)
	c := stubSessionSeams(t, backend.SessionInfo{Exists: true, Running: true, Hash: "h", ImageID: "old"}, "h")
	backendImageID = func(engine, image string) string { return "new" }

	code, _, errs := run("exec", "feat", "--src", project, "--", "true")
	if code != 0 {
		t.Fatalf("exec = %d, %s", code, errs)
	}
	if len(c.run) != 1 || len(c.exec) != 0 || len(c.ensure) != 0 {
		t.Errorf("calls: run=%d exec=%d ensure=%d, want 1/0/0", len(c.run), len(c.exec), len(c.ensure))
	}
	if !strings.Contains(errs, "stale (image rebuilt)") {
		t.Errorf("missing image-rebuilt notice:\n%s", errs)
	}
}

// TestExecImageFreshPrefixTolerant: docker reports the container's image as
// "sha256:<hex>" while `image inspect` IDs may come bare (and vice versa on
// podman) — a prefix-only difference must still route to the session.
func TestExecImageFreshPrefixTolerant(t *testing.T) {
	project := sessionProject(t)
	c := stubSessionSeams(t, backend.SessionInfo{Exists: true, Running: true, Hash: "h", ImageID: "abc"}, "h")
	backendImageID = func(engine, image string) string { return "sha256:abc" }

	code, _, errs := run("exec", "feat", "--src", project, "--", "true")
	if code != 0 {
		t.Fatalf("exec = %d, %s", code, errs)
	}
	if len(c.exec) != 1 || len(c.run) != 0 {
		t.Errorf("calls: exec=%d run=%d, want 1/0", len(c.exec), len(c.run))
	}
	if strings.Contains(errs, "stale") {
		t.Errorf("unexpected stale notice:\n%s", errs)
	}
}

// TestExecNoSessionFallsBack: with no running session, exec runs one-shot
// silently — no notice, no session creation.
func TestExecNoSessionFallsBack(t *testing.T) {
	project := sessionProject(t)
	c := stubSessionSeams(t, backend.SessionInfo{}, "h")

	code, _, errs := run("exec", "feat", "--src", project, "--", "true")
	if code != 0 {
		t.Fatalf("exec = %d, %s", code, errs)
	}
	if len(c.run) != 1 || len(c.exec) != 0 || len(c.ensure) != 0 {
		t.Errorf("calls: run=%d exec=%d ensure=%d, want 1/0/0", len(c.run), len(c.exec), len(c.ensure))
	}
	if strings.Contains(errs, "stale") {
		t.Errorf("unexpected stale notice:\n%s", errs)
	}
}

// TestExecUnknownSessionMode: enter's fail-fast twin for exec — a typo in
// SANDBOXER_SESSION errors out before any backend call.
func TestExecUnknownSessionMode(t *testing.T) {
	project := sessionProject(t)
	c := stubSessionSeams(t, backend.SessionInfo{}, "h")
	t.Setenv("SANDBOXER_SESSION", "bogus")

	code, _, errs := run("exec", "feat", "--src", project, "--", "true")
	if code != 1 || !strings.Contains(errs, "unknown session mode") {
		t.Errorf("bogus mode = (%d, %q)", code, errs)
	}
	if len(c.ensure)+len(c.exec)+len(c.run)+len(c.inspect) != 0 {
		t.Error("no backend call may happen on an unknown session mode")
	}
}

// TestExecSessionErrorPropagates: an ExecSession failure surfaces (exit 1 with
// the engine error printed) and does NOT fall back to a one-shot run.
func TestExecSessionErrorPropagates(t *testing.T) {
	project := sessionProject(t)
	c := stubSessionSeams(t, backend.SessionInfo{Exists: true, Running: true, Hash: "h"}, "h")
	backendExecSession = func(o backend.RunOpts, name string, cmdArgs []string) (int, error) {
		return 0, errors.New("engine exploded")
	}

	code, _, errs := run("exec", "feat", "--src", project, "--", "true")
	if code != 1 || !strings.Contains(errs, "engine exploded") {
		t.Errorf("failing session exec = (%d, %q), want exit 1 with the engine error", code, errs)
	}
	if len(c.run) != 0 {
		t.Error("a failing session exec must not fall back to a one-shot run")
	}
}

// TestExecEphemeralSkipsInspect: --ephemeral bypasses session discovery
// entirely.
func TestExecEphemeralSkipsInspect(t *testing.T) {
	project := sessionProject(t)
	c := stubSessionSeams(t, backend.SessionInfo{Exists: true, Running: true, Hash: "h"}, "h")

	code, _, errs := run("exec", "feat", "--src", project, "--ephemeral", "--", "true")
	if code != 0 {
		t.Fatalf("exec = %d, %s", code, errs)
	}
	if len(c.inspect) != 0 || len(c.run) != 1 || len(c.exec) != 0 {
		t.Errorf("calls: inspect=%d run=%d exec=%d, want 0/1/0", len(c.inspect), len(c.run), len(c.exec))
	}
}
