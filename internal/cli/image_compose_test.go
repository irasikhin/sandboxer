package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
)

func TestParseRunArgv(t *testing.T) {
	argv := []string{
		"run", "--rm", "-i", "-t",
		"--user", "1000:1000", "--cap-drop=ALL",
		"--security-opt", "no-new-privileges", "--userns=keep-id",
		"--workdir", "/work", "--memory", "2G", "--cpus", "1.5",
		"--network", "sbxnet", "--volume", "/a:/a:rw",
		"--env", "HOME=/h", "--env", "BAD",
		"img:latest", "bash", "-l",
	}
	svc := parseRunArgv(argv)
	if svc.Image != "img:latest" {
		t.Errorf("image = %q", svc.Image)
	}
	if !svc.StdinOpen || !svc.Tty {
		t.Error("expected stdin_open + tty")
	}
	if svc.User != "1000:1000" || svc.WorkingDir != "/work" || svc.MemLimit != "2G" || svc.CPUs != "1.5" {
		t.Errorf("scalars wrong: %+v", svc)
	}
	if len(svc.CapDrop) != 1 || svc.CapDrop[0] != "ALL" {
		t.Errorf("cap_drop = %v", svc.CapDrop)
	}
	if svc.Environment["HOME"] != "/h" {
		t.Errorf("env HOME = %q", svc.Environment["HOME"])
	}
	if _, ok := svc.Environment["BAD"]; ok {
		t.Error("malformed --env without = should be skipped")
	}
	if len(svc.Command) != 2 || svc.Command[0] != "bash" {
		t.Errorf("command = %v", svc.Command)
	}
	// A dangling flag at the end must be guarded (no panic, no value).
	_ = parseRunArgv([]string{"run", "--user"})
}

func TestComposeEngineError(t *testing.T) {
	project := newProject(t)
	fakePodman(t)
	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}
	// Engine no longer discoverable → compose fails at engine resolution.
	t.Setenv("PATH", "")
	t.Setenv("SANDBOXER_ENGINE", "")
	if code, _, errs := run("compose", "feat", "--src", project, "--backend", "podman"); code != 1 ||
		!strings.Contains(errs, "podman or docker") {
		t.Errorf("compose engine-error = (%d, %q)", code, errs)
	}
}

func TestShellQuoteArg(t *testing.T) {
	for in, want := range map[string]string{
		"plain": "plain",
		"a b":   "'a b'",
		"it's":  `'it'\''s'`,
		"":      "''",
	} {
		if got := shellQuoteArg(in); got != want {
			t.Errorf("shellQuoteArg(%q) = %q, want %q", in, got, want)
		}
	}
	if got := shellLine([]string{"podman", "run", "a b"}); got != "podman run 'a b'" {
		t.Errorf("shellLine = %q", got)
	}
}

func TestComposeYAMLEgress(t *testing.T) {
	argv := []string{"run", "--rm", "img", "bash"}
	on, err := composeYAML("s", config.Runtime{Egress: true, Domains: []string{"a.com"}}, argv, "")
	if err != nil || !strings.Contains(on, "a.com") {
		t.Errorf("egress-on doc missing domains (err=%v):\n%s", err, on)
	}
	off, err := composeYAML("s", config.Runtime{}, argv, "")
	if err != nil || !strings.Contains(off, "egress disabled") {
		t.Errorf("egress-off note missing (err=%v):\n%s", err, off)
	}
	if strings.Contains(off, "session container") {
		t.Errorf("ephemeral doc must not carry a session note:\n%s", off)
	}
	// A session name adds the managed-container note; the YAML body is unchanged.
	withNote, err := composeYAML("s", config.Runtime{}, argv, "sandboxer-s-abcd1234")
	if err != nil || !strings.Contains(withNote, "sandboxer-s-abcd1234") ||
		!strings.Contains(withNote, "one-shot (ephemeral) equivalent") {
		t.Errorf("session note missing (err=%v):\n%s", err, withNote)
	}
}

func TestComposeCommand(t *testing.T) {
	project := newProject(t)
	fakePodman(t)
	t.Setenv("SANDBOXER_SESSION", "") // assert the persistent default below
	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}

	// Default: docker-compose.yml. The session mode defaults to persistent, so
	// the header notes the managed session container the YAML does not reproduce.
	code, out, errs := run("compose", "feat", "--src", project, "--backend", "podman")
	if code != 0 {
		t.Fatalf("compose = %d %s", code, errs)
	}
	for _, want := range []string{"services:", "feat:", "image:", "working_dir:", "command:", "egress",
		"sandboxer-feat-", "one-shot (ephemeral) equivalent"} {
		if !strings.Contains(out, want) {
			t.Errorf("compose YAML missing %q:\n%s", want, out)
		}
	}

	// --ephemeral --print-run: the plain one-shot run command line, no session.
	code, out2, errs := run("compose", "feat", "--src", project, "--backend", "podman", "--print-run", "--ephemeral")
	if code != 0 {
		t.Fatalf("compose --print-run = %d %s", code, errs)
	}
	if !strings.Contains(out2, "podman run --rm") || !strings.Contains(out2, "bash") {
		t.Errorf("print-run missing run command:\n%s", out2)
	}
	if strings.Contains(out2, "sleep infinity") {
		t.Errorf("ephemeral print-run must not show the session create:\n%s", out2)
	}

	// --ephemeral YAML: no session note.
	if code, out3, errs := run("compose", "feat", "--src", project, "--backend", "podman", "--ephemeral"); code != 0 ||
		strings.Contains(out3, "session container") {
		t.Errorf("ephemeral compose = (%d, %s):\n%s", code, errs, out3)
	}

	// The removed native backend is rejected.
	if code, _, errs := run("compose", "feat", "--src", project, "--backend", "native"); code != 1 ||
		!strings.Contains(errs, "native backend was removed") {
		t.Errorf("compose native = (%d, %q)", code, errs)
	}
}

// TestComposePrintRunPersistent: in persistent mode --print-run emits the
// create + exec pair from backend.CreateArgv/ExecArgv — the same no-drift
// builders the real session lifecycle uses.
func TestComposePrintRunPersistent(t *testing.T) {
	project := newProject(t)
	fakePodman(t)
	t.Setenv("SANDBOXER_SESSION", "")
	t.Setenv("TERM", "xterm") // execArgv forwards TERM; pin it for the assert
	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}

	code, out, errs := run("compose", "feat", "--src", project, "--backend", "podman", "--print-run")
	if code != 0 {
		t.Fatalf("compose --print-run = %d %s", code, errs)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("want a create + exec pair (2 lines), got %d:\n%s", len(lines), out)
	}
	for _, want := range []string{"podman run -d --init", "--name sandboxer-feat-",
		"--label sandboxer.managed=true", "--label sandboxer.hash=", "sleep infinity"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("create line missing %q:\n%s", want, lines[0])
		}
	}
	for _, want := range []string{"podman exec -i", "sandboxer-feat-", "--env TERM=xterm", "bash -l"} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("exec line missing %q:\n%s", want, lines[1])
		}
	}

	// SANDBOXER_SESSION=ephemeral (the operator kill-switch) restores the
	// single one-shot line.
	t.Setenv("SANDBOXER_SESSION", "ephemeral")
	if code, out2, errs := run("compose", "feat", "--src", project, "--backend", "podman", "--print-run"); code != 0 ||
		strings.Contains(out2, "sleep infinity") || !strings.Contains(out2, "podman run --rm") {
		t.Errorf("ephemeral-env print-run = (%d, %s):\n%s", code, errs, out2)
	}

	// A typo'd session mode fails fast, before any engine work.
	t.Setenv("SANDBOXER_SESSION", "bogus")
	if code, _, errs := run("compose", "feat", "--src", project, "--backend", "podman"); code != 1 ||
		!strings.Contains(errs, "unknown session mode") {
		t.Errorf("bogus session mode = (%d, %q)", code, errs)
	}
}

// TestComposePrintRunHashSelfConsistent: the sandboxer.hash label on the
// printed create line is the hash of exactly the argv printed (the dynamic
// egress flags are documented, not reproduced — so with egress on, the label
// must NOT inherit the real session's egress-flavored hash). Recomputing the
// ConfigHash from the printed tokens (run -d --init + everything but the
// name/labels) must reproduce the label value.
func TestComposePrintRunHashSelfConsistent(t *testing.T) {
	project := newProject(t)
	fakePodman(t)
	t.Setenv("SANDBOXER_SESSION", "")
	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}

	code, out, errs := run("compose", "feat", "--src", project, "--backend", "podman", "--print-run")
	if code != 0 {
		t.Fatalf("compose --print-run = %d %s", code, errs)
	}
	createLine := strings.Split(strings.TrimSpace(out), "\n")[0]
	// No printed token needs shell quoting in this fixture, so Fields is an
	// exact inverse of shellLine.
	if strings.Contains(createLine, "'") {
		t.Fatalf("fixture grew a quoted token, the reconstruction below is invalid:\n%s", createLine)
	}
	tokens := strings.Fields(createLine)[1:] // drop the engine
	var core []string
	label := ""
	for i := 0; i < len(tokens); i++ {
		switch tokens[i] {
		case "--name":
			i++
		case "--label":
			i++
			if v, ok := strings.CutPrefix(tokens[i], "sandboxer.hash="); ok {
				label = v
			}
		default:
			core = append(core, tokens[i])
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(core, "\x00")))
	if want := hex.EncodeToString(sum[:]); label != want {
		t.Errorf("printed hash label %q is not the hash of the printed argv %q", label, want)
	}
}

// TestComposePrintRunMCPDomains: compose folds the profile's MCP-server
// domains into the printed allowlist env, same as a real enter/exec.
func TestComposePrintRunMCPDomains(t *testing.T) {
	project := newProject(t)
	fakePodman(t)
	t.Setenv("SANDBOXER_SESSION", "")
	cfg := filepath.Join(t.TempDir(), "p.yaml")
	// context7's domains are NOT in the default allowlist, so this cannot
	// pass vacuously.
	if err := os.WriteFile(cfg, []byte("name: feat\nmcp: [context7]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("create", "--src", project, "--config", cfg); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}

	code, out, errs := run("compose", "feat", "--src", project, "--backend", "podman", "--print-run")
	if code != 0 {
		t.Fatalf("compose --print-run = %d %s", code, errs)
	}
	if !strings.Contains(out, "mcp.context7.com") {
		t.Errorf("printed argv missing the MCP server's domain:\n%s", out)
	}

	// An unresolvable mcp: entry fails compose the same way enter would fail.
	if err := os.WriteFile(cfg, []byte("name: feat\nmcp: [no-such-server]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("compose", "--src", project, "--config", cfg, "--backend", "podman"); code != 1 ||
		!strings.Contains(errs, "unknown MCP server") {
		t.Errorf("compose with a bogus MCP server = (%d, %q)", code, errs)
	}
}

func TestComposeNoSandbox(t *testing.T) {
	project := newProject(t)
	// No slug and no created sandbox → resolveTarget fails before any engine work.
	if code, _, errs := run("compose", "--src", project); code != 1 || !strings.Contains(errs, "no sandbox selected") {
		t.Errorf("compose no-sandbox = (%d, %q)", code, errs)
	}
}

func TestBuildImageNoEngine(t *testing.T) {
	t.Setenv("SANDBOXER_ENGINE", "")
	t.Setenv("SANDBOXER_BACKEND", "podman")
	t.Setenv("PATH", "") // neither podman nor docker discoverable
	if code, _, errs := run("build-image"); code != 1 || !strings.Contains(errs, "podman or docker") {
		t.Errorf("build-image no-engine = (%d, %q)", code, errs)
	}
}

func TestBuildImageCommand(t *testing.T) {
	requireExec(t, "sh")
	newProject(t) // sets IN_CONTAINER off, isolates profile store
	fakePodman(t) // podman on PATH, exits 0 for every subcommand

	// With a no-op engine, build → load → (no retag) all succeed.
	if code, _, errs := run("build-image", "--engine", "podman"); code != 0 {
		t.Fatalf("build-image = %d %s", code, errs)
	}

	// With a profile (-f): the profile's content-addressed variant tag is
	// built instead of the stock default (the progress banner names it).
	cfg := filepath.Join(t.TempDir(), "img.yaml")
	if err := os.WriteFile(cfg, []byte("name: feat\nimage:\n  extraPkgs: [ripgrep]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errs := run("build-image", "--engine", "podman", "-f", cfg)
	if code != 0 {
		t.Fatalf("build-image -f = %d %s", code, errs)
	}
	if !strings.Contains(errs, "sandboxer-toolbox:var-") {
		t.Errorf("expected a var- variant build, got: %s", errs)
	}

	// A multi-profile file: the positional names the section to build.
	multi := filepath.Join(t.TempDir(), "multi.yaml")
	if err := os.WriteFile(multi,
		[]byte("profiles:\n  web:\n    tools: [node]\n  plain: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errs = run("build-image", "web", "--engine", "podman", "-f", multi)
	if code != 0 || !strings.Contains(errs, "sandboxer-toolbox:var-") {
		t.Errorf("build-image multi-profile = (%d, %q), want a var- build", code, errs)
	}

	// A positional that names no profile fails before any engine work.
	if code, _, errs := run("build-image", "no-such-profile", "--engine", "podman"); code != 1 ||
		!strings.Contains(errs, "no profile") {
		t.Errorf("build-image bogus profile = (%d, %q)", code, errs)
	}
}
