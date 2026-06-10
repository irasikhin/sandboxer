package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/toolbox"
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

// TestComposePrintRunImageVariant: an image-customized profile's printed
// create line names the spec's content-addressed var- tag, not the stock
// default — compose must show the image a real enter would run.
func TestComposePrintRunImageVariant(t *testing.T) {
	project := newProject(t)
	fakePodman(t)
	t.Setenv("SANDBOXER_SESSION", "")
	cfg := filepath.Join(t.TempDir(), "p.yaml")
	if err := os.WriteFile(cfg, []byte("name: feat\nimage:\n  extraPkgs: [ripgrep]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("create", "--src", project, "--config", cfg); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}

	code, out, errs := run("compose", "feat", "--src", project, "--backend", "podman", "--print-run")
	if code != 0 {
		t.Fatalf("compose --print-run = %d %s", code, errs)
	}
	createLine := strings.Split(strings.TrimSpace(out), "\n")[0]
	if !strings.Contains(createLine, "sandboxer-toolbox:var-") {
		t.Errorf("create line must carry the variant tag:\n%s", createLine)
	}
	if strings.Contains(createLine, config.LoadDefaults().Image) {
		t.Errorf("create line must not fall back to the stock image:\n%s", createLine)
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

	// A profile whose image.nix is missing fails spec resolution fast.
	noNix := filepath.Join(t.TempDir(), "no-nix.yaml")
	if err := os.WriteFile(noNix, []byte("name: feat\nimage:\n  nix: missing.nix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("build-image", "--engine", "podman", "-f", noNix); code != 1 ||
		!strings.Contains(errs, "image.nix") {
		t.Errorf("build-image missing image.nix = (%d, %q)", code, errs)
	}

	// A malformed profile file fails the document load.
	broken := filepath.Join(t.TempDir(), "broken.yaml")
	if err := os.WriteFile(broken, []byte("image: [not a map\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("build-image", "--engine", "podman", "-f", broken); code != 1 || errs == "" {
		t.Errorf("build-image malformed profile = (%d, %q)", code, errs)
	}
}

// pinPodman puts a podman on PATH that serves the latest-pin resolver: any run
// with a bind-mounted /out dir gets rev files written there (the argv carries
// the real temp path, same trick as the toolbox fake-engine tests); everything
// else exits 0.
func pinPodman(t *testing.T, rev string) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\n" +
		"out=\"\"\n" +
		"for a in \"$@\"; do case \"$a\" in *:/out:rw) out=\"${a%%:*}\";; esac; done\n" +
		"if [ -n \"$out\" ]; then echo " + rev + " > \"$out/rev.nixpkgs\"; echo " + rev + " > \"$out/rev.llm-agents\"; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "podman"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SANDBOXER_ENGINE", "")
}

// TestBuildImageRevFlags covers the one-shot rev flags end to end through the
// build seam: validation fails fast (before any engine), "latest" resolves
// once and stamps the pins cache, a warm stamp needs no resolver, --refresh
// forces a re-resolve, and a concrete flag rev overrides the profile's value.
func TestBuildImageRevFlags(t *testing.T) {
	requireExec(t, "sh")
	newProject(t)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	// A malformed rev is rejected by the ValidateImageSpec rules before
	// profile or engine work.
	if code, _, errs := run("build-image", "--llm-agents-rev", "ZZZ"); code != 1 ||
		!strings.Contains(errs, "image.llmAgentsRev") {
		t.Errorf("bad --llm-agents-rev = (%d, %q)", code, errs)
	}
	if code, _, errs := run("build-image", "--nixpkgs-rev", "also bad"); code != 1 ||
		!strings.Contains(errs, "image.nixpkgsRev") {
		t.Errorf("bad --nixpkgs-rev = (%d, %q)", code, errs)
	}

	// Stub the build seam and capture what the command hands it.
	var captured toolbox.BuildOpts
	oldBuild := toolboxBuild
	defer func() { toolboxBuild = oldBuild }()
	toolboxBuild = func(o toolbox.BuildOpts) error { captured = o; return nil }

	rev := strings.Repeat("d", 40)
	pinPodman(t, rev)
	code, _, errs := run("build-image", "--engine", "podman", "--nixpkgs-rev", "latest")
	if code != 0 {
		t.Fatalf("build-image latest = %d %s", code, errs)
	}
	// Bare rev flags build a variant nothing selects by default — the command
	// must say so instead of implying the stock image moved.
	if !strings.Contains(errs, "note: built variant") {
		t.Errorf("bare-flag variant build must print the not-the-stock-image note, got: %s", errs)
	}
	if captured.Spec.NixpkgsRev != rev {
		t.Errorf("seam spec rev = %q, want the resolved %s", captured.Spec.NixpkgsRev, rev)
	}
	if captured.Image != captured.Spec.Tag() || !strings.HasPrefix(captured.Image, "sandboxer-toolbox:var-") {
		t.Errorf("seam image = %q, want the pinned spec's var- tag", captured.Image)
	}
	pins, err := toolbox.LoadPins()
	if err != nil || pins["nixpkgs"].Rev != rev {
		t.Errorf("stamped pins = %+v, %v; want nixpkgs %s", pins, err, rev)
	}

	// Warm stamp: a plain no-op podman (no resolver) still builds — the pins
	// cache is hit, never re-resolved.
	fakePodman(t)
	if code, _, errs := run("build-image", "--engine", "podman", "--nixpkgs-rev", "latest"); code != 0 {
		t.Errorf("warm-stamp build-image = %d %s", code, errs)
	}

	// --refresh forces a re-resolve; the no-op podman writes no rev files, so
	// the resolve fails loudly instead of silently reusing the stamp.
	if code, _, errs := run("build-image", "--engine", "podman", "--nixpkgs-rev", "latest", "--refresh"); code != 1 ||
		!strings.Contains(errs, "resolve latest") {
		t.Errorf("refresh with a dead resolver = (%d, %q), want a resolve failure", code, errs)
	}

	// A concrete flag rev is a one-shot override of the profile's "latest" —
	// no resolver involved, the spec carries the flag's commit.
	cfg := filepath.Join(t.TempDir(), "p.yaml")
	if err := os.WriteFile(cfg, []byte("name: feat\nimage:\n  nixpkgsRev: latest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	override := strings.Repeat("e", 40)
	if code, _, errs := run("build-image", "--engine", "podman", "-f", cfg, "--nixpkgs-rev", override); code != 0 {
		t.Fatalf("build-image concrete override = %d %s", code, errs)
	}
	if captured.Spec.NixpkgsRev != override {
		t.Errorf("override spec rev = %q, want %s", captured.Spec.NixpkgsRev, override)
	}
}
