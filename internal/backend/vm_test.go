package backend

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
)

// TestVMCreateArgv pins the exact create argv for a non-narrowed sandbox:
// `machine create --name … -I image` then the shared block — identity-mapped
// volumes, the identity env, and the machine size — with NO keepalive command
// and NO credential (auth travels per exec).
func TestVMCreateArgv(t *testing.T) {
	o := RunOpts{
		MountDest: true, Image: "img:1", Dest: "/d", Slug: "s", BaseDir: "/b",
		HomeDir: "/d/.home", Mem: "2G", CPU: "150%",
		AuthEnv: []string{"CLAUDE_CODE_OAUTH_TOKEN=t"},
		Stdin:   strings.NewReader(""), Stdout: &bytes.Buffer{},
	}
	got := vmCreateArgv(o, "sandboxer-s-deadbeef")
	want := []string{
		"machine", "create", "--name", "sandboxer-s-deadbeef", "-I", "img:1",
		"-w", "/d", "-v", "/d:/d",
		"-e", "SANDBOXER_IN_CONTAINER=1",
		"-e", "SANDBOXER_SLUG=s", "-e", "SANDBOXER_SANDBOX_DIR=/d",
		"-e", "LANG=C.UTF-8",
		"-e", "HOME=/d/.home", "-v", "/d/.home:/d/.home",
		"--mem", "2048", "--cpus", "2", "--net",
	}
	if !slices.Equal(got, want) {
		t.Errorf("vmCreateArgv =\n%q\nwant\n%q", got, want)
	}
	// The create argv must carry no credential — the token is a per-exec secret.
	if j := strings.Join(got, " "); strings.Contains(j, "CLAUDE_CODE_OAUTH_TOKEN") || strings.Contains(j, "=t") {
		t.Errorf("create argv leaks a credential: %q", j)
	}
}

// TestVMCreateArgvNarrowed pins that a narrowed sandbox never shares the
// sandbox root (the containment boundary) and instead shares each source dir at
// its own identity-mapped path, with the mount-gen env folded in.
func TestVMCreateArgvNarrowed(t *testing.T) {
	o := RunOpts{
		MountDest: false, Image: "img:1", Dest: "/d", Slug: "s",
		SrcMounts: []string{"/d/svc/a", "/d/svc/b"}, MountGen: "mg1",
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{},
	}
	got := vmCreateArgv(o, "n")
	joined := strings.Join(got, " ")
	if strings.Contains(joined, "-v /d:/d") {
		t.Errorf("narrowed sandbox must NOT share the root: %q", joined)
	}
	for _, m := range o.SrcMounts {
		if !strings.Contains(joined, "-v "+m+":"+m) {
			t.Errorf("missing source share %q in %q", m, joined)
		}
	}
	if !strings.Contains(joined, "-e SANDBOXER_MOUNT_GEN=mg1") {
		t.Errorf("missing mount-gen env in %q", joined)
	}
}

// TestVMExecArgv pins the exec argv (with and without a host TERM), the -t rule
// on a non-TTY stdio, and the --secret-env reference channel for credentials.
func TestVMExecArgv(t *testing.T) {
	o := RunOpts{
		Dest: "/d", AuthEnv: []string{"ANTHROPIC_API_KEY=k"},
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{},
	}

	t.Setenv("TERM", "xterm-256color")
	got := vmExecArgv(o, "n", []string{"claude", "--continue"})
	want := []string{
		"machine", "exec", "--name", "n", "-i", "-w", "/d",
		"-e", "TERM=xterm-256color",
		"--secret-env", "ANTHROPIC_API_KEY=ANTHROPIC_API_KEY",
		"--", "claude", "--continue",
	}
	if !slices.Equal(got, want) {
		t.Errorf("vmExecArgv with TERM =\n%q\nwant\n%q", got, want)
	}
	// No -t on non-TTY buffers, and the secret VALUE never enters argv.
	if slices.Contains(got, "-t") {
		t.Error("vmExecArgv added -t on non-TTY stdio")
	}
	if strings.Contains(strings.Join(got, " "), "=k") {
		t.Error("vmExecArgv leaked the secret value into argv")
	}

	t.Setenv("TERM", "")
	got = vmExecArgv(RunOpts{Dest: "/d", Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}}, "n", []string{"sh"})
	want = []string{"machine", "exec", "--name", "n", "-i", "-w", "/d", "--", "sh"}
	if !slices.Equal(got, want) {
		t.Errorf("vmExecArgv without TERM =\n%q\nwant\n%q", got, want)
	}
}

// TestVMRunArgv pins the one-shot ephemeral argv: interactive flags, the
// credential secret-env, the common block, then `--` and the agent command.
func TestVMRunArgv(t *testing.T) {
	o := RunOpts{
		Interactive: true, MountDest: true, Image: "img:1", Dest: "/d", Slug: "s",
		Args: []string{"bash", "-l"}, Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{},
	}
	got := vmRunArgv(o)
	want := []string{
		"machine", "run", "-I", "img:1", "-i",
		"-w", "/d", "-v", "/d:/d",
		"-e", "SANDBOXER_IN_CONTAINER=1",
		"-e", "SANDBOXER_SLUG=s", "-e", "SANDBOXER_SANDBOX_DIR=/d",
		"-e", "LANG=C.UTF-8",
		"--mem", "4096", "--cpus", "2", "--net",
		"--", "bash", "-l",
	}
	if !slices.Equal(got, want) {
		t.Errorf("vmRunArgv =\n%q\nwant\n%q", got, want)
	}
}

// TestVMLifecycleArgv pins the tiny start/stop/remove/list builders, including
// the -f that a non-interactive delete needs.
func TestVMLifecycleArgv(t *testing.T) {
	for _, c := range []struct {
		name string
		got  []string
		want []string
	}{
		{"start", vmStartArgv("n"), []string{"machine", "start", "--name", "n"}},
		{"stop", vmStopArgv("n"), []string{"machine", "stop", "--name", "n"}},
		{"remove", vmRemoveArgv("n"), []string{"machine", "delete", "--name", "n", "-f"}},
		{"list", vmListArgv(), []string{"machine", "ls", "--json"}},
	} {
		if !slices.Equal(c.got, c.want) {
			t.Errorf("%s argv = %q, want %q", c.name, c.got, c.want)
		}
	}
}

// TestVMExtraMountsAndEnv pins the profile passthrough in smolvm's dialect: rw
// leaves the mode off, ro keeps it, and env keys are sorted.
func TestVMExtraMountsAndEnv(t *testing.T) {
	p := &config.Profile{
		ExtraMounts: []config.Mount{
			{Source: "/h/a", Target: "/g/a"},
			{Source: "/h/b", Target: "/g/b", Mode: "ro"},
		},
		Env: map[string]string{"Z": "1", "A": "2"},
	}
	got := vmExtraMountsAndEnv(p)
	want := []string{
		"-v", "/h/a:/g/a",
		"-v", "/h/b:/g/b:ro",
		"-e", "A=2", "-e", "Z=1",
	}
	if !slices.Equal(got, want) {
		t.Errorf("vmExtraMountsAndEnv =\n%q\nwant\n%q", got, want)
	}
	if vmExtraMountsAndEnv(nil) != nil {
		t.Error("nil profile must yield nil")
	}
}

// TestVMMemMiB pins the memory-unit conversion and the default/fallback.
func TestVMMemMiB(t *testing.T) {
	for in, want := range map[string]string{
		"":         "4096", // default
		"2G":       "2048",
		"512M":     "512",
		"1536M":    "1536",
		"1g":       "1024",
		"2097152k": "2048",
		"garbage":  "4096", // unparseable → default
	} {
		if got := vmMemMiB(in); got != want {
			t.Errorf("vmMemMiB(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestVMCPUs pins the vCPU conversion: fractions round up, the default applies
// when unset, and a systemd percentage is honored.
func TestVMCPUs(t *testing.T) {
	for in, want := range map[string]string{
		"":     "2", // default
		"1":    "1",
		"4":    "4",
		"1.5":  "2", // round up
		"150%": "2", // 1.5 → 2
		"250%": "3", // 2.5 → 3
	} {
		if got := vmCPUs(in); got != want {
			t.Errorf("vmCPUs(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestVMNetworkArgs pins the three outbound states and their fail-closed
// default, and that the allowlist folds into the session hash.
func TestVMNetworkArgs(t *testing.T) {
	// egress on + allowlist → sorted --allow-host, subdomain dots stripped.
	on := RunOpts{RT: config.Runtime{Egress: true, Domains: []string{".example.com", "api.test", "example.com"}}}
	got := vmNetworkArgs(on)
	want := []string{"--allow-host", "api.test", "--allow-host", "example.com"}
	if !slices.Equal(got, want) {
		t.Errorf("allowlist net = %q, want %q", got, want)
	}

	// egress on + empty allowlist → offline (no flag).
	if g := vmNetworkArgs(RunOpts{RT: config.Runtime{Egress: true}}); len(g) != 0 {
		t.Errorf("empty allowlist net = %q, want none (offline)", g)
	}

	// egress off → open network.
	if g := vmNetworkArgs(RunOpts{RT: config.Runtime{Egress: false}}); !slices.Equal(g, []string{"--net"}) {
		t.Errorf("egress-off net = %q, want [--net]", g)
	}
	// SANDBOXER_NO_EGRESS overrides an enabled allowlist → open network.
	if g := vmNetworkArgs(RunOpts{NoEgress: true, RT: config.Runtime{Egress: true, Domains: []string{"x"}}}); !slices.Equal(g, []string{"--net"}) {
		t.Errorf("NoEgress net = %q, want [--net]", g)
	}

	// The allowlist is inside the session hash: editing domains recreates.
	base := RunOpts{Image: "i", Dest: "/d", RT: config.Runtime{Egress: true, Domains: []string{"a.com"}}}
	more := base
	more.RT.Domains = []string{"a.com", "b.com"}
	if vmSessionWantHash(base) == vmSessionWantHash(more) {
		t.Error("editing the allowlist did not change the session hash")
	}
}

// TestResolveSmolvmMissing pins the fail-loud behavior when smolvm is absent:
// microvm never falls back to a container engine, and the error carries an
// install hint.
func TestResolveSmolvmMissing(t *testing.T) {
	t.Setenv("SANDBOXER_SMOLVM", "/nonexistent/smolvm-definitely-not-here")
	_, err := ResolveEngine("microvm", config.Defaults{Engine: "docker"})
	if err == nil {
		t.Fatal("microvm with a missing smolvm must error, not fall back to docker")
	}
	if !strings.Contains(err.Error(), "smolvm") {
		t.Errorf("error should mention smolvm: %v", err)
	}
}
