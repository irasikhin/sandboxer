package cli

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/backend"
	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/registry"
	"github.com/irasikhin/sandboxer/internal/sandbox"
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
	idle    []string          // names probed for tmux idleness
	sync    []string          // names whose saved layout was refreshed post-attach
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
	oldIdle, oldTTY, oldSync := backendSessionIdle, backendIsTerminal, backendSyncSessionState
	t.Cleanup(func() {
		backendEnsureSession, backendExecSession, backendRun = oldEnsure, oldExec, oldRun
		backendInspectSession, backendWantHash, backendImageID = oldInspect, oldHash, oldImageID
		backendSessionIdle, backendIsTerminal, backendSyncSessionState = oldIdle, oldTTY, oldSync
	})
	backendSyncSessionState = func(engine, name, statePath string) {
		c.sync = append(c.sync, name)
	}
	// Default: the session holds a live tmux session. The destructive verdict
	// is the one a test must ask for explicitly.
	backendSessionIdle = func(engine, name string) bool {
		c.idle = append(c.idle, name)
		return false
	}
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
	fakeMsb(t)
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
	if len(argv) != 3 || argv[0] != "bash" || argv[1] != "-c" ||
		!strings.Contains(argv[2], "tmux -L sandboxer new-session -A -s main") {
		t.Errorf("exec argv = %v, want the tmux attach launcher", argv)
	}
	// The attach returned → the saved layout is refreshed (capture-on-detach).
	if len(c.sync) != 1 || c.sync[0] != name {
		t.Errorf("sync = %v, want one refresh of %q after the attach", c.sync, name)
	}
	for _, want := range []string{
		name, "DETACHES", "ENDS that tmux session", "sandboxer enter feat", "sandboxer: done in",
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

// TestEnterSessionFlag: --session opens a separate named tmux session in the
// same container, and an unsafe name is rejected before any backend call —
// the name is spliced into the launcher script.
func TestEnterSessionFlag(t *testing.T) {
	project := sessionProject(t)
	c := stubSessionSeams(t, backend.SessionInfo{}, "h")

	if code, _, errs := run("enter", "feat", "--src", project, "--session", "side"); code != 0 {
		t.Fatalf("enter --session = %d, %s", code, errs)
	}
	if len(c.exec) != 1 || !strings.Contains(c.exec[0][2], "new-session -A -s side") {
		t.Errorf("exec argv = %v, want the side session attached", c.exec)
	}

	code, _, errs := run("enter", "feat", "--src", project, "--session", "a;b")
	if code != 1 || !strings.Contains(errs, "invalid --session") {
		t.Errorf("unsafe session name = (%d, %q), want a validation error", code, errs)
	}
	if len(c.exec) != 1 || len(c.run) != 0 {
		t.Error("an invalid session name must not reach the backend")
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
		// A one-shot container has no persistent session to record.
		if len(c.sync) != 0 {
			t.Errorf("sync = %v, want none on the one-shot path", c.sync)
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
		fakeMsb(t)
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
	if len(c.sync) != 0 {
		t.Error("no attach happened, so no layout refresh may happen")
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
	if got := c.exec[0]; !slices.Equal(got, podmanSocketPrefix([]string{"echo", "hi"})) {
		t.Errorf("exec argv = %v, want the podman-socket-wrapped echo hi", got)
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

// TestExecExitCodePassthrough: the child's non-zero exit code becomes
// sandboxer's own exit code (not a flattened 1), with nothing printed about
// it — the child's output already told the story.
func TestExecExitCodePassthrough(t *testing.T) {
	project := sessionProject(t)
	stubSessionSeams(t, backend.SessionInfo{}, "h")
	backendRun = func(o backend.RunOpts) (int, error) { return 7, nil }

	code, _, errs := run("exec", "feat", "--src", project, "--", "false")
	if code != 7 {
		t.Errorf("exec exit = %d, want the child's 7 (stderr: %q)", code, errs)
	}
	if strings.Contains(errs, "exited") {
		t.Errorf("a passed-through exit code must not be narrated, got %q", errs)
	}
}

// TestExecSessionExitCodePassthrough: same passthrough on the session path.
func TestExecSessionExitCodePassthrough(t *testing.T) {
	project := sessionProject(t)
	stubSessionSeams(t, backend.SessionInfo{Exists: true, Running: true, Hash: "h"}, "h")
	backendExecSession = func(o backend.RunOpts, name string, cmdArgs []string) (int, error) {
		return 42, nil
	}

	code, _, _ := run("exec", "feat", "--src", project, "--", "sh", "-c", "exit 42")
	if code != 42 {
		t.Errorf("session exec exit = %d, want the child's 42", code)
	}
}

// TestEnterExitCodePassthrough: enter passes the shell's exit code through
// too, like ssh does.
func TestEnterExitCodePassthrough(t *testing.T) {
	project := sessionProject(t)
	stubSessionSeams(t, backend.SessionInfo{}, "h")
	backendExecSession = func(o backend.RunOpts, name string, cmdArgs []string) (int, error) {
		return 5, nil
	}

	code, _, errs := run("enter", "feat", "--src", project)
	if code != 5 {
		t.Errorf("enter exit = %d, want the shell's 5 (stderr: %q)", code, errs)
	}
}

// TestExecQuiet: -q drops the narration (config line, src sync chatter) while
// a security warning still reaches stderr.
func TestExecQuiet(t *testing.T) {
	project := sessionProject(t)
	stubSessionSeams(t, backend.SessionInfo{}, "h")

	code, _, errs := run("exec", "feat", "--src", project, "-q", "--", "true")
	if code != 0 {
		t.Fatalf("exec -q = %d, %s", code, errs)
	}
	if strings.Contains(errs, "backend=") {
		t.Errorf("exec -q must drop the config line, got %q", errs)
	}

	// The open-egress warning is not narration: it prints despite -q.
	cfg := filepath.Join(t.TempDir(), "open.nix")
	if err := os.WriteFile(cfg, []byte(`{ name = "feat"; srcs = [ { src = "."; branch = "feat/x"; } ]; egress = { enabled = false; }; }`), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errs = run("exec", "--src", project, "--config", cfg, "-q", "--", "true")
	if code != 0 {
		t.Fatalf("exec -q (open egress) = %d, %s", code, errs)
	}
	if !strings.Contains(errs, "WARNING — egress is unrestricted") {
		t.Errorf("exec -q must keep the security warning, got %q", errs)
	}
}

// TestEnterQuiet: --quiet enters silently — no config line, no banner, no
// epilogue — and still exits clean.
func TestEnterQuiet(t *testing.T) {
	project := sessionProject(t)
	stubSessionSeams(t, backend.SessionInfo{}, "h")

	code, _, errs := run("enter", "feat", "--src", project, "--quiet")
	if code != 0 {
		t.Fatalf("enter --quiet = %d, %s", code, errs)
	}
	for _, banned := range []string{"backend=", "persistent session", "done in", "sandboxer: src "} {
		if strings.Contains(errs, banned) {
			t.Errorf("enter --quiet must drop %q, got %q", banned, errs)
		}
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

// TestEnterStaleBusySessionAttaches is the regression guard for the session
// loss this whole path exists to prevent. A running-but-stale session used to
// be sidestepped into a `run --rm` container, where tmux is the main process —
// so Ctrl-Space d destroyed everything in it — while the real session stayed
// running and unreachable, and NOTHING ever converged it, so every later enter
// did the same. When the session holds a tmux session, enter must attach to it
// (exec, no ensure — ensure would recreate the stale container) and print the
// persistent detach semantics plus how to apply the pending config.
func TestEnterStaleBusySessionAttaches(t *testing.T) {
	project := sessionProject(t)
	c := stubSessionSeams(t, backend.SessionInfo{Exists: true, Running: true, Hash: "h", ImageID: "old"}, "h")
	backendImageID = func(engine, image string) string { return "new" }

	code, _, errs := run("enter", "feat", "--src", project)
	if code != 0 {
		t.Fatalf("enter = %d, %s", code, errs)
	}
	if len(c.exec) != 1 || len(c.run) != 0 || len(c.ensure) != 0 {
		t.Fatalf("calls: exec=%d run=%d ensure=%d, want an attach (1/0/0)", len(c.exec), len(c.run), len(c.ensure))
	}
	if c.execTo[0] != backend.SessionName("feat", config.StateDir(project)) {
		t.Errorf("attached to %q, want the session container", c.execTo[0])
	}
	// The stale attach is still an attach: the layout is refreshed after it.
	if len(c.sync) != 1 {
		t.Errorf("sync = %v, want one refresh after the stale attach", c.sync)
	}
	if strings.Contains(errs, "one-shot") || strings.Contains(errs, "--rm") {
		t.Errorf("a stale busy session must never be sidestepped into a one-shot:\n%s", errs)
	}
	for _, want := range []string{
		"attaching as-is", "image rebuilt", "DETACHES",
		"sandboxer stop feat && sandboxer enter feat",
	} {
		if !strings.Contains(errs, want) {
			t.Errorf("stale-attach output missing %q:\n%s", want, errs)
		}
	}
}

// TestEnterStaleIdleSessionConverges: the other half of the same policy. A
// stale session that holds no tmux session has nothing to lose, so enter
// converges it (EnsureSession recreates) instead of leaving it stale forever —
// without this the config change never lands and the trap is permanent.
func TestEnterStaleIdleSessionConverges(t *testing.T) {
	project := sessionProject(t)
	c := stubSessionSeams(t, backend.SessionInfo{Exists: true, Running: true, Hash: "old"}, "want")
	backendSessionIdle = func(engine, name string) bool {
		c.idle = append(c.idle, name)
		return true
	}

	code, _, errs := run("enter", "feat", "--src", project)
	if code != 0 {
		t.Fatalf("enter = %d, %s", code, errs)
	}
	if len(c.ensure) != 1 || len(c.exec) != 1 || len(c.run) != 0 {
		t.Fatalf("calls: ensure=%d exec=%d run=%d, want a converge (1/1/0)", len(c.ensure), len(c.exec), len(c.run))
	}
	if len(c.idle) != 1 {
		t.Errorf("idleness probed %d times, want exactly 1", len(c.idle))
	}
	if strings.Contains(errs, "attaching as-is") || strings.Contains(errs, "one-shot") {
		t.Errorf("an idle stale session should be converged silently:\n%s", errs)
	}
}

// TestEnterFreshSessionSkipsIdleProbe: the probe is an engine round-trip and
// only the stale verdict needs it — a fresh (or absent) session must not pay
// for it.
func TestEnterFreshSessionSkipsIdleProbe(t *testing.T) {
	project := sessionProject(t)
	c := stubSessionSeams(t, backend.SessionInfo{Exists: true, Running: true, Hash: "h"}, "h")

	if code, _, errs := run("enter", "feat", "--src", project); code != 0 {
		t.Fatalf("enter = %d, %s", code, errs)
	}
	if len(c.idle) != 0 {
		t.Errorf("idleness probed for a fresh session: %v", c.idle)
	}
}

// TestEnterEphemeralBannerNamesTheSwitch: a deliberate one-shot run still has
// to warn that detaching ends it, and say WHICH switch chose ephemeral — the
// env kill-switch outranks the profile, so the cause is often somewhere the
// user is not looking.
func TestEnterEphemeralBannerNamesTheSwitch(t *testing.T) {
	project := sessionProject(t)
	stubSessionSeams(t, backend.SessionInfo{}, "h")

	code, _, errs := run("enter", "feat", "--src", project, "--ephemeral")
	if code != 0 {
		t.Fatalf("enter --ephemeral = %d, %s", code, errs)
	}
	for _, want := range []string{"one-shot", "--ephemeral", "ENDS it"} {
		if !strings.Contains(errs, want) {
			t.Errorf("ephemeral output missing %q:\n%s", want, errs)
		}
	}
	if strings.Contains(errs, "sandboxer stop feat") {
		t.Errorf("an explicitly ephemeral run should not offer a way back:\n%s", errs)
	}
}

// --- mount drift -------------------------------------------------------------

// driftIDs is a recorded mount set naming a directory the current resolve does
// not produce — the sandboxes under test are unnarrowed, so their current set
// is empty and every recorded path reads as gone. That is a real drift shape
// (a view dir the host deleted), and it drives the same mountDriftWhy path as
// an added or recreated one; the per-kind matrix is pinned in the sandbox
// package, where it needs no engine at all.
var driftIDs = sandbox.EncodeMountIDs([]sandbox.MountID{{Path: "/p/services/api", ID: "8:1:42"}})

// mountAwareHash makes the stubbed want-hash seam behave like the real one for
// the purpose of the "was it ONLY the mounts?" discriminator: the hash the
// session recorded is reproduced exactly when the recorded mount identities are
// substituted back in, and differs otherwise.
func mountAwareHash(c *sessionCalls, recorded string) func(backend.RunOpts) string {
	return func(o backend.RunOpts) string {
		c.hash = append(c.hash, o)
		if o.MountIDs == recorded {
			return "old" // the hash the session recorded, reproduced exactly
		}
		return "new"
	}
}

// TestEnterStaleBusySessionNamesMountDrift: the whole point of recording the
// mount identities. A narrowed sandbox re-expands its includes against the live
// worktree on every enter, so the mount set moves when the HOST moves — and
// reporting that as "profile changed" told the user to look at a file they
// never touched. Name what actually moved instead.
func TestEnterStaleBusySessionNamesMountDrift(t *testing.T) {
	project := sessionProject(t)
	c := stubSessionSeams(t, backend.SessionInfo{Exists: true, Running: true, Hash: "old", Mounts: driftIDs}, "new")
	backendWantHash = mountAwareHash(c, driftIDs)

	code, _, errs := run("enter", "feat", "--src", project)
	if code != 0 {
		t.Fatalf("enter = %d, %s", code, errs)
	}
	if !strings.Contains(errs, "mounts moved: - /p/services/api (gone)") {
		t.Errorf("output does not name the drift:\n%s", errs)
	}
	if strings.Contains(errs, "profile changed") {
		t.Errorf("a pure mount drift must not be blamed on the profile:\n%s", errs)
	}
	if len(c.exec) != 1 || len(c.ensure) != 0 {
		t.Errorf("calls: exec=%d ensure=%d, want an attach (1/0)", len(c.exec), len(c.ensure))
	}
}

// TestEnterMountDriftAlsoNamesProfileChange: the mount set and the profile can
// move together, and naming only the mounts would be a fresh instance of the
// bug being fixed. The discriminator rehashes with the RECORDED identities
// substituted back in — a still-mismatched hash means something else moved too.
func TestEnterMountDriftAlsoNamesProfileChange(t *testing.T) {
	project := sessionProject(t)
	stubSessionSeams(t, backend.SessionInfo{Exists: true, Running: true, Hash: "old", Mounts: driftIDs}, "new")
	// Never reproduces the recorded hash: the profile moved as well.
	backendWantHash = func(o backend.RunOpts) string { return "new" }

	_, _, errs := run("enter", "feat", "--src", project)
	for _, want := range []string{"mounts moved:", "the profile also changed"} {
		if !strings.Contains(errs, want) {
			t.Errorf("output missing %q:\n%s", want, errs)
		}
	}
}

// TestEnterStaleBusySessionUnknownMountsFallsBack: a session created before the
// mounts label existed has no baseline. It must degrade to the honest old
// answer, never invent a diff from an absent one.
func TestEnterStaleBusySessionUnknownMountsFallsBack(t *testing.T) {
	project := sessionProject(t)
	stubSessionSeams(t, backend.SessionInfo{Exists: true, Running: true, Hash: "old"}, "new")

	_, _, errs := run("enter", "feat", "--src", project)
	if !strings.Contains(errs, "profile changed") {
		t.Errorf("no baseline should read as a profile change:\n%s", errs)
	}
	if strings.Contains(errs, "mounts moved") {
		t.Errorf("a diff was invented from an absent baseline:\n%s", errs)
	}
}

// TestEnterMountDriftPromptRecreates: a busy session whose mounts moved is the
// one stale shape that is already broken — its bind mounts name directories the
// host replaced. Answering yes rebuilds it.
func TestEnterMountDriftPromptRecreates(t *testing.T) {
	project := sessionProject(t)
	c := stubSessionSeams(t, backend.SessionInfo{Exists: true, Running: true, Hash: "old", Mounts: driftIDs}, "new")
	backendWantHash = mountAwareHash(c, driftIDs)
	backendIsTerminal = func(any) bool { return true }

	code, _, errs := runIn("y\n", "enter", "feat", "--src", project)
	if code != 0 {
		t.Fatalf("enter = %d, %s", code, errs)
	}
	if !strings.Contains(errs, "Rebuild the session now?") {
		t.Errorf("no prompt was shown:\n%s", errs)
	}
	if len(c.ensure) != 1 || len(c.exec) != 1 {
		t.Fatalf("calls: ensure=%d exec=%d, want a converge (1/1)", len(c.ensure), len(c.exec))
	}
	if strings.Contains(errs, "attaching as-is") {
		t.Errorf("accepted the rebuild but still attached as-is:\n%s", errs)
	}
}

// TestEnterMountDriftPromptDeclined: anything but yes changes nothing — enter
// attaches exactly as it did before the prompt existed.
func TestEnterMountDriftPromptDeclined(t *testing.T) {
	project := sessionProject(t)
	c := stubSessionSeams(t, backend.SessionInfo{Exists: true, Running: true, Hash: "old", Mounts: driftIDs}, "new")
	backendWantHash = mountAwareHash(c, driftIDs)
	backendIsTerminal = func(any) bool { return true }

	_, _, errs := runIn("n\n", "enter", "feat", "--src", project)
	if len(c.ensure) != 0 || len(c.exec) != 1 {
		t.Fatalf("calls: ensure=%d exec=%d, want an attach (0/1)", len(c.ensure), len(c.exec))
	}
	if !strings.Contains(errs, "attaching as-is") {
		t.Errorf("declining should attach as-is:\n%s", errs)
	}
}

// TestEnterMountDriftNonTTYNeverPrompts: a scripted enter must never block on a
// question nobody is there to read. --recreate stays the non-interactive answer.
func TestEnterMountDriftNonTTYNeverPrompts(t *testing.T) {
	project := sessionProject(t)
	c := stubSessionSeams(t, backend.SessionInfo{Exists: true, Running: true, Hash: "old", Mounts: driftIDs}, "new")
	backendWantHash = mountAwareHash(c, driftIDs)

	_, _, errs := runIn("y\n", "enter", "feat", "--src", project)
	if strings.Contains(errs, "Rebuild the session now?") {
		t.Errorf("prompted without a terminal:\n%s", errs)
	}
	if len(c.ensure) != 0 || len(c.exec) != 1 {
		t.Errorf("calls: ensure=%d exec=%d, want an attach (0/1)", len(c.ensure), len(c.exec))
	}
}

// TestExecStaleSessionNamesMountDrift: exec gets the same accurate diagnosis
// and never the prompt — its one-shot already runs against the current mounts.
func TestExecStaleSessionNamesMountDrift(t *testing.T) {
	project := sessionProject(t)
	c := stubSessionSeams(t, backend.SessionInfo{Exists: true, Running: true, Hash: "old", Mounts: driftIDs}, "new")
	backendWantHash = mountAwareHash(c, driftIDs)
	backendIsTerminal = func(any) bool { return true }

	_, _, errs := runIn("y\n", "exec", "feat", "--src", project, "--", "true")
	if !strings.Contains(errs, "mounts moved: - /p/services/api (gone)") {
		t.Errorf("exec does not name the drift:\n%s", errs)
	}
	if strings.Contains(errs, "Rebuild the session now?") {
		t.Errorf("exec must never prompt:\n%s", errs)
	}
	if len(c.run) != 1 {
		t.Errorf("exec should have fallen back to one-shot, run=%d", len(c.run))
	}
}
