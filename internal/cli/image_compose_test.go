package cli

import (
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
	on, err := composeYAML("s", config.Runtime{Egress: true, Domains: []string{"a.com"}}, argv)
	if err != nil || !strings.Contains(on, "a.com") {
		t.Errorf("egress-on doc missing domains (err=%v):\n%s", err, on)
	}
	off, err := composeYAML("s", config.Runtime{}, argv)
	if err != nil || !strings.Contains(off, "egress disabled") {
		t.Errorf("egress-off note missing (err=%v):\n%s", err, off)
	}
}

func TestComposeCommand(t *testing.T) {
	project := newProject(t)
	fakePodman(t)
	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}

	// Default: docker-compose.yml.
	code, out, errs := run("compose", "feat", "--src", project, "--backend", "podman")
	if code != 0 {
		t.Fatalf("compose = %d %s", code, errs)
	}
	for _, want := range []string{"services:", "feat:", "image:", "working_dir:", "command:", "egress"} {
		if !strings.Contains(out, want) {
			t.Errorf("compose YAML missing %q:\n%s", want, out)
		}
	}

	// --print-run: a docker/podman run command line.
	code, out2, errs := run("compose", "feat", "--src", project, "--backend", "podman", "--print-run")
	if code != 0 {
		t.Fatalf("compose --print-run = %d %s", code, errs)
	}
	if !strings.Contains(out2, "podman run") || !strings.Contains(out2, "bash") {
		t.Errorf("print-run missing run command:\n%s", out2)
	}

	// The removed native backend is rejected.
	if code, _, errs := run("compose", "feat", "--src", project, "--backend", "native"); code != 1 ||
		!strings.Contains(errs, "native backend was removed") {
		t.Errorf("compose native = (%d, %q)", code, errs)
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
}
