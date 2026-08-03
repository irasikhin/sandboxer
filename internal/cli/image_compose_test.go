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
		// The nestedContainers flags: --device/--cap-add treated as the image
		// used to silently truncate everything after them.
		"--device", "/dev/fuse", "--cap-add", "SETUID",
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
	if len(svc.Devices) != 1 || svc.Devices[0] != "/dev/fuse" {
		t.Errorf("devices = %v", svc.Devices)
	}
	if len(svc.CapAdd) != 1 || svc.CapAdd[0] != "SETUID" {
		t.Errorf("cap_add = %v", svc.CapAdd)
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
		!strings.Contains(errs, "docker or podman") {
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
	// The git-worktree mount injects GIT_CONFIG_VALUE_0=* which shellLine
	// single-quotes; no printed token contains an internal space, so Fields still
	// splits cleanly and each token just needs unquoting to recover the raw argv.
	tokens := strings.Fields(createLine)[1:] // drop the engine
	for i := range tokens {
		tokens[i] = shellUnquoteToken(tokens[i])
	}
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

// shellUnquoteToken reverses shellQuoteArg for a single whitespace-free token: a
// single-quote-wrapped token has its wrapper stripped and any '\” escape turned
// back into a literal quote.
func shellUnquoteToken(s string) string {
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return strings.ReplaceAll(s[1:len(s)-1], `'\''`, "'")
	}
	return s
}

// TestComposePrintRunImageVariant: an image-customized profile's printed
// create line names the spec's content-addressed var- tag, not the stock
// default — compose must show the image a real enter would run.
func TestComposePrintRunImageVariant(t *testing.T) {
	project := newProject(t)
	fakePodman(t)
	warmPins(t, strings.Repeat("a", 40))
	t.Setenv("SANDBOXER_SESSION", "")
	cfg := filepath.Join(t.TempDir(), "p.nix")
	if err := os.WriteFile(cfg, []byte("{ name = \"feat\"; srcs = [ { src = \".\"; branch = \"feat/x\"; } ]; image.packages = [ \"ripgrep\" ]; }\n"), 0o644); err != nil {
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
	if code, _, errs := run("image", "build"); code != 1 || !strings.Contains(errs, "docker or podman") {
		t.Errorf("build-image no-engine = (%d, %q)", code, errs)
	}
}

func TestBuildImageCommand(t *testing.T) {
	requireExec(t, "sh")
	newProject(t)                         // sets IN_CONTAINER off
	pinPodman(t, strings.Repeat("f", 40)) // podman serves the pin resolver, exits 0 otherwise

	// The default build re-resolves the input revs (auto-update), then
	// build → load → (no retag) all succeed.
	if code, _, errs := run("image", "build", "--engine", "podman"); code != 0 {
		t.Fatalf("build-image = %d %s", code, errs)
	}

	// With a profile (-f): the profile's content-addressed variant tag is
	// built instead of the stock default (the progress banner names it).
	cfg := filepath.Join(t.TempDir(), "img.nix")
	if err := os.WriteFile(cfg, []byte("{ name = \"feat\"; srcs = [ { src = \".\"; branch = \"feat/x\"; } ]; image.packages = [ \"ripgrep\" ]; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errs := run("image", "build", "--engine", "podman", "-f", cfg)
	if code != 0 {
		t.Fatalf("build-image -f = %d %s", code, errs)
	}
	if !strings.Contains(errs, "sandboxer-toolbox:var-") {
		t.Errorf("expected a var- variant build, got: %s", errs)
	}

	// A multi-profile file: the positional names the section to build.
	multi := filepath.Join(t.TempDir(), "multi.nix")
	if err := os.WriteFile(multi,
		[]byte("{ profiles = { web.tools = [ \"node\" ]; plain = { }; }; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errs = run("image", "build", "web", "--engine", "podman", "-f", multi)
	if code != 0 || !strings.Contains(errs, "sandboxer-toolbox:var-") {
		t.Errorf("build-image multi-profile = (%d, %q), want a var- build", code, errs)
	}

	// A positional that names no profile fails before any engine work.
	if code, _, errs := run("image", "build", "no-such-profile", "--engine", "podman"); code != 1 ||
		!strings.Contains(errs, "no profile") {
		t.Errorf("build-image bogus profile = (%d, %q)", code, errs)
	}

	// A profile whose image.overlay file is missing fails spec resolution fast.
	noNix := filepath.Join(t.TempDir(), "no-nix.nix")
	if err := os.WriteFile(noNix, []byte("{ name = \"feat\"; image.overlay = \"missing.nix\"; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("image", "build", "--engine", "podman", "-f", noNix); code != 1 ||
		!strings.Contains(errs, "image.overlay") {
		t.Errorf("build-image missing image.overlay = (%d, %q)", code, errs)
	}

	// A malformed profile file fails the document load.
	broken := filepath.Join(t.TempDir(), "broken.nix")
	if err := os.WriteFile(broken, []byte("{ image = [ \"not-a-map\" ]; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("image", "build", "--engine", "podman", "-f", broken); code != 1 || errs == "" {
		t.Errorf("build-image malformed profile = (%d, %q)", code, errs)
	}
}

// pinPodman puts a podman on PATH that serves the latest-pin resolver: any run
// with a bind-mounted /out dir gets rev files written there (the argv carries
// the real temp path, same trick as the toolbox fake-engine tests); everything
// else exits 0. The pins cache is isolated so no test stamps the user's real
// one.
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
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("SANDBOXER_ENGINE", "")
}

// warmPins isolates the pins cache and stamps both inputs, so a command that
// resolves a variant image never launches a resolver container in tests.
func warmPins(t *testing.T, rev string) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := toolbox.SavePins(toolbox.Pins{
		"nixpkgs":    {Ref: "refs/heads/nixos-unstable", Rev: rev},
		"llm-agents": {Ref: "HEAD", Rev: rev},
	}); err != nil {
		t.Fatal(err)
	}
}

// TestBuildImageRevFlags covers the rev plumbing end to end through the build
// seam: validation fails fast (before any engine), the default build
// re-resolves the tracking revs and stamps the pins cache while keeping the
// STOCK tag, --no-refresh builds from the warm stamp without a resolver, the
// default refresh fails loudly when the resolver is dead, and a concrete flag
// rev builds a variant.
func TestBuildImageRevFlags(t *testing.T) {
	requireExec(t, "sh")
	newProject(t)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	// A malformed rev is rejected by the ValidateImageSpec rules before
	// profile or engine work.
	if code, _, errs := run("image", "build", "--llm-agents-rev", "ZZZ"); code != 1 ||
		!strings.Contains(errs, "image.llmAgentsRev") {
		t.Errorf("bad --llm-agents-rev = (%d, %q)", code, errs)
	}
	if code, _, errs := run("image", "build", "--nixpkgs-rev", "also bad"); code != 1 ||
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
	code, _, errs := run("image", "build", "--engine", "podman")
	if code != 0 {
		t.Fatalf("build-image = %d %s", code, errs)
	}
	// The default build IS the auto-update: revs resolved and handed to the
	// build, under the stock tag ("latest" is the default, not a variant), and
	// no variant note.
	if strings.Contains(errs, "note: built variant") {
		t.Errorf("the default build must not print the variant note: %s", errs)
	}
	if captured.Spec.NixpkgsRev != rev || captured.Spec.LLMAgentsRev != rev {
		t.Errorf("seam spec revs = %q/%q, want the resolved %s", captured.Spec.NixpkgsRev, captured.Spec.LLMAgentsRev, rev)
	}
	if captured.Image != config.LoadDefaults().Image {
		t.Errorf("seam image = %q, want the stock default", captured.Image)
	}
	pins, err := toolbox.LoadPins()
	if err != nil || pins["nixpkgs"].Rev != rev {
		t.Errorf("stamped pins = %+v, %v; want nixpkgs %s", pins, err, rev)
	}
	// An explicit "latest" flag is the same default, spelled out — still the
	// stock tag, still no note.
	if code, _, errs := run("image", "build", "--engine", "podman", "--nixpkgs-rev", "latest"); code != 0 ||
		strings.Contains(errs, "note: built variant") {
		t.Errorf("explicit latest = (%d, %q), want a plain stock build", code, errs)
	}
	if captured.Image != config.LoadDefaults().Image {
		t.Errorf("explicit-latest image = %q, want the stock default", captured.Image)
	}

	// --no-refresh: a plain no-op podman (no resolver) still builds — the
	// warm stamp is used, never re-resolved.
	fakePodman(t)
	if code, _, errs := run("image", "build", "--engine", "podman", "--no-refresh"); code != 0 {
		t.Errorf("no-refresh build-image = %d %s", code, errs)
	}
	if captured.Spec.NixpkgsRev != rev {
		t.Errorf("no-refresh spec rev = %q, want the stamped %s", captured.Spec.NixpkgsRev, rev)
	}

	// The default refresh re-resolves; this no-op podman writes no rev files,
	// so the resolve fails loudly instead of silently reusing the stamp.
	if code, _, errs := run("image", "build", "--engine", "podman"); code != 1 ||
		!strings.Contains(errs, "resolve latest") {
		t.Errorf("default refresh with a dead resolver = (%d, %q), want a resolve failure", code, errs)
	}

	// A concrete bare-flag rev is a pin → a variant nothing selects by
	// default; the command says so instead of implying the stock image moved.
	override := strings.Repeat("e", 40)
	if code, _, errs := run("image", "build", "--engine", "podman", "--no-refresh",
		"--nixpkgs-rev", override, "--llm-agents-rev", override); code != 0 ||
		!strings.Contains(errs, "note: built variant") {
		t.Fatalf("concrete bare-flag build = (%d, %q), want the variant note", code, errs)
	}
	if captured.Spec.NixpkgsRev != override {
		t.Errorf("override spec rev = %q, want %s", captured.Spec.NixpkgsRev, override)
	}
	if captured.Image != captured.Spec.Tag() || !strings.HasPrefix(captured.Image, "sandboxer-toolbox:var-") {
		t.Errorf("seam image = %q, want the pinned spec's var- tag", captured.Image)
	}

	// A concrete flag rev is a one-shot override of the profile's tracking
	// value; the other input still resolves from the warm stamp.
	cfg := filepath.Join(t.TempDir(), "p.nix")
	if err := os.WriteFile(cfg, []byte("{ name = \"feat\"; image.nixpkgsRev = \"latest\"; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("image", "build", "--engine", "podman", "--no-refresh", "-f", cfg, "--nixpkgs-rev", override); code != 0 {
		t.Fatalf("build-image concrete override = %d %s", code, errs)
	}
	if captured.Spec.NixpkgsRev != override || captured.Spec.LLMAgentsRev != rev {
		t.Errorf("override spec revs = %q/%q, want %s/%s", captured.Spec.NixpkgsRev, captured.Spec.LLMAgentsRev, override, rev)
	}
}
