package backend

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/toolbox"
)

// TestMSBCreateArgv pins the exact create argv for a non-narrowed sandbox:
// `create --name …` then the identity labels, the shared block (shares, identity
// env, size) and the image as the TRAILING positional — with no credential on
// it, because auth travels per exec.
func TestMSBCreateArgv(t *testing.T) {
	o := RunOpts{
		MountDest: true, Image: "img:1", Dest: "/d", Slug: "s", BaseDir: "/b",
		HomeDir: "/d/.home", Mem: "2G", CPU: "150%", MountIDs: "mid",
		AuthEnv: []string{"CLAUDE_CODE_OAUTH_TOKEN=t"},
		Stdin:   strings.NewReader(""), Stdout: &bytes.Buffer{},
	}
	got := msbCreateArgv(o, "sandboxer-s-deadbeef", "h1")
	want := []string{
		"create", "--name", "sandboxer-s-deadbeef",
		"--label", "sandboxer.managed=true",
		"--label", "sandboxer.slug=s",
		"--label", "sandboxer.base=/b",
		"--label", "sandboxer.hash=h1",
		"--label", "sandboxer.mounts=mid",
		"-w", "/d", "-v", "/d:/d",
		"-e", "SANDBOXER_IN_CONTAINER=1",
		"-e", "SANDBOXER_SLUG=s", "-e", "SANDBOXER_SANDBOX_DIR=/d",
		"-e", "LANG=C.UTF-8",
		"-e", "DOCKER_HOST=unix:///var/run/docker.sock",
		"-e", "TESTCONTAINERS_RYUK_DISABLED=true",
		"-e", "HOME=/d/.home", "-v", "/d/.home:/d/.home",
		"-m", "2048M", "-c", "2",
		"img:1",
	}
	if !slices.Equal(got, want) {
		t.Errorf("msbCreateArgv =\n%q\nwant\n%q", got, want)
	}
	if j := strings.Join(got, " "); strings.Contains(j, "CLAUDE_CODE_OAUTH_TOKEN") {
		t.Errorf("create argv leaks a credential: %q", j)
	}
}

// TestMSBCreateArgvNarrowed pins the containment boundary: a narrowed sandbox
// never shares its root, only the individual source dirs, identity-mapped.
func TestMSBCreateArgvNarrowed(t *testing.T) {
	o := RunOpts{
		Image: "img:1", Dest: "/d", Slug: "s",
		SrcMounts: []string{"/d/svc/a", "/d/svc/b"}, MountGen: "mg1", DestGen: "g2",
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{},
	}
	joined := strings.Join(msbCreateArgv(o, "n", "h"), " ")
	if strings.Contains(joined, "-v /d:/d") {
		t.Errorf("narrowed sandbox must NOT share the root: %q", joined)
	}
	for _, m := range o.SrcMounts {
		if !strings.Contains(joined, "-v "+m+":"+m) {
			t.Errorf("missing source share %q in %q", m, joined)
		}
	}
	for _, e := range []string{"-e SANDBOXER_MOUNT_GEN=mg1", "-e SANDBOXER_SANDBOX_GEN=g2"} {
		if !strings.Contains(joined, e) {
			t.Errorf("missing %q in %q", e, joined)
		}
	}
}

// TestMSBCreateArgvGitMounts pins the opt-in git share: identity-mapped like a
// source (the worktree's .git names its git dir by absolute HOST path, so only
// the same path resolves inside the guest), with :ro appended for a read-only
// one — and absent entirely when no source asked for it.
func TestMSBCreateArgvGitMounts(t *testing.T) {
	base := RunOpts{
		MountDest: true, Image: "img:1", Dest: "/d", Slug: "s",
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{},
	}
	if j := strings.Join(msbCreateArgv(base, "n", "h"), " "); strings.Contains(j, ".git") {
		t.Errorf("a default sandbox shares no git dir, got %q", j)
	}

	ro := base
	ro.GitMounts = []config.Mount{{Source: "/repo/.git", Target: "/repo/.git", Mode: "ro"}}
	if j := strings.Join(msbCreateArgv(ro, "n", "h"), " "); !strings.Contains(j, "-v /repo/.git:/repo/.git:ro") {
		t.Errorf("read-only git share missing or writable in %q", j)
	}

	rw := base
	rw.GitMounts = []config.Mount{{Source: "/repo/.git", Target: "/repo/.git", Mode: "rw"}}
	j := strings.Join(msbCreateArgv(rw, "n", "h"), " ")
	if !strings.Contains(j, "-v /repo/.git:/repo/.git") || strings.Contains(j, "/repo/.git:ro") {
		t.Errorf("read-write git share wrong in %q", j)
	}

	// The share is part of the machine's configuration, so flipping it must
	// rebuild rather than leave a live session with a git dir it should not
	// have (or should).
	if vmSessionWantHash(ro) == vmSessionWantHash(base) {
		t.Error("sharing a git dir did not flip the session hash")
	}
	if vmSessionWantHash(rw) == vmSessionWantHash(ro) {
		t.Error("ro → rw did not flip the session hash")
	}
}

// TestMSBGitMountPreflight: a git dir under /tmp is shadowed by the guest's
// tmpfs exactly like any other share, and must be reported as such instead of
// silently arriving empty.
func TestMSBGitMountPreflight(t *testing.T) {
	o := RunOpts{
		Image: "img:1", Dest: "/d", Slug: "s", HomeDir: "/d/.home",
		GitMounts: []config.Mount{{Source: "/tmp/r/.git", Target: "/tmp/r/.git", Mode: "ro"}},
		Stdin:     strings.NewReader(""), Stdout: &bytes.Buffer{},
	}
	err := msbPreflight(o)
	if err == nil || !strings.Contains(err.Error(), "/tmp/r/.git") {
		t.Fatalf("msbPreflight = %v, want the shadowed git share named", err)
	}
}

// TestMSBHashArgv pins the hash contract: the name and the labels are excluded
// (a rename or a relabel must never recreate a machine), while a real
// configuration change flips it.
func TestMSBHashArgv(t *testing.T) {
	o := RunOpts{
		Engine: msbEngine, Image: "img:1", Dest: "/d", Slug: "s", MountDest: true,
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{},
	}
	if j := strings.Join(msbHashArgv(o), " "); strings.Contains(j, "--name") || strings.Contains(j, "--label") {
		t.Errorf("hash argv must exclude the name and labels: %q", j)
	}
	base := vmSessionWantHash(o)

	renamed := o
	renamed.MountIDs = "different-mounts" // a label only
	if vmSessionWantHash(renamed) != base {
		t.Error("the mounts label flipped the session hash")
	}

	resized := o
	resized.Mem = "8G"
	if vmSessionWantHash(resized) == base {
		t.Error("a memory change did not flip the session hash")
	}
}

// TestMSBNetworkArgs pins the four egress postures.
func TestMSBNetworkArgs(t *testing.T) {
	tests := []struct {
		name string
		o    RunOpts
		want []string
	}{
		{
			name: "allowlist",
			o:    RunOpts{RT: config.Runtime{Egress: true, Domains: []string{"github.com", ".example.com", "github.com", " "}}},
			want: []string{
				"--no-net",
				"--net-rule", "allow@*.example.com:tcp:80,allow@*.example.com:tcp:443",
				"--net-rule", "allow@*.github.com:tcp:80,allow@*.github.com:tcp:443",
			},
		},
		{
			name: "empty allowlist is fully offline",
			o:    RunOpts{RT: config.Runtime{Egress: true}},
			want: []string{"--no-net"},
		},
		{
			name: "egress off is open",
			o:    RunOpts{RT: config.Runtime{Egress: false}},
			want: nil,
		},
		{
			// The COMBINED WALL: default-deny + the allowlist + one door on the
			// proxy's own host:port. Direct traffic is enforced by the VM; only
			// what rides the proxy is the proxy's to constrain.
			name: "proxy plus allowlist is the combined wall",
			o: RunOpts{RT: config.Runtime{Egress: true, Proxy: "http://p.corp.example:8080",
				NoProxy: "localhost", Domains: []string{"github.com"}}},
			want: []string{
				"-e", "HTTP_PROXY=http://p.corp.example:8080", "-e", "http_proxy=http://p.corp.example:8080",
				"-e", "HTTPS_PROXY=http://p.corp.example:8080", "-e", "https_proxy=http://p.corp.example:8080",
				"-e", "NO_PROXY=localhost", "-e", "no_proxy=localhost",
				"--no-net",
				"--net-rule", "allow@*.github.com:tcp:80,allow@*.github.com:tcp:443",
				"--net-rule", "allow@p.corp.example:tcp:8080",
			},
		},
		{
			// The guest's 127.0.0.1 is its own stack, so a loopback proxy is
			// rewritten to msb's host alias and its door is the host group on
			// the proxy port. No allowlist = only the door: all egress rides
			// the proxy.
			name: "loopback proxy without domains is proxy-only egress",
			o:    RunOpts{RT: config.Runtime{Egress: true, Proxy: "http://127.0.0.1:8888"}},
			want: []string{
				"-e", "HTTP_PROXY=http://host.microsandbox.internal:8888",
				"-e", "http_proxy=http://host.microsandbox.internal:8888",
				"-e", "HTTPS_PROXY=http://host.microsandbox.internal:8888",
				"-e", "https_proxy=http://host.microsandbox.internal:8888",
				"--no-net",
				"--net-rule", "allow@host:tcp:8888",
			},
		},
		{
			// Egress disabled but a proxy configured: open network + env — a
			// routing convenience with no wall. The loopback door still needs
			// opening, and any explicit rule replaces the implicit open
			// default, so public is restated in the same token.
			name: "egress off keeps the proxy on an open network",
			o:    RunOpts{RT: config.Runtime{Egress: false, Proxy: "http://127.0.0.1:8888"}},
			want: []string{
				"-e", "HTTP_PROXY=http://host.microsandbox.internal:8888",
				"-e", "http_proxy=http://host.microsandbox.internal:8888",
				"-e", "HTTPS_PROXY=http://host.microsandbox.internal:8888",
				"-e", "https_proxy=http://host.microsandbox.internal:8888",
				"--net-rule", "allow@public,allow@host:tcp:8888",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := msbNetworkArgs(tt.o); !slices.Equal(got, tt.want) {
				t.Errorf("msbNetworkArgs =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

// TestMSBNetTargetsKeepSubdomains pins the suffix property: an allowlist entry
// covers the domain AND its subdomains — never a narrowing to exact hosts.
func TestMSBNetTargetsKeepSubdomains(t *testing.T) {
	got := msbNetTargets([]string{"cloudfront.net", ".github.com", "cloudfront.net", " "})
	want := []string{"*.cloudfront.net", "*.github.com"}
	if !slices.Equal(got, want) {
		t.Errorf("msbNetTargets = %q, want %q", got, want)
	}
}

// TestMSBAuthEnvDefault pins the default credential channel: values travel with
// the exec (never on the create argv), and no --secret reference is emitted.
func TestMSBAuthEnvDefault(t *testing.T) {
	o := RunOpts{
		Dest: "/d", AuthEnv: []string{"ANTHROPIC_API_KEY=k"},
		RT:    config.Runtime{Egress: true, Domains: []string{"api.anthropic.com"}},
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{},
	}
	t.Setenv("TERM", "xterm-256color")
	got := msbExecArgv(o, "n", []string{"claude", "--continue"})
	want := []string{
		"exec", "-w", "/d", "-e", "TERM=xterm-256color",
		"-e", "ANTHROPIC_API_KEY=k", "n", "--", "claude", "--continue",
	}
	if !slices.Equal(got, want) {
		t.Errorf("msbExecArgv =\n%q\nwant\n%q", got, want)
	}
	// msb exec has NO -i flag (stdin is piped by default) — passing it is a
	// hard argv error on msb 0.6.x.
	if slices.Contains(got, "-i") {
		t.Errorf("msbExecArgv = %q, must not carry -i (not in msb's grammar)", got)
	}
	if len(msbSecretArgs(o)) != 0 {
		t.Error("--secret must be opt-in")
	}
}

// TestMSBSecretsMode pins the opt-in host-scoped secret channel: the create argv
// carries only KEY references scoped to the allowlist, and the per-exec value is
// dropped so it cannot override the guest's stand-in.
func TestMSBSecretsMode(t *testing.T) {
	t.Setenv(msbSecretsEnv, "1")
	o := RunOpts{
		Dest: "/d", AuthEnv: []string{"ANTHROPIC_API_KEY=k"},
		RT:    config.Runtime{Egress: true, Domains: []string{"api.anthropic.com", "github.com"}},
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{},
	}
	want := []string{
		"--secret", "ANTHROPIC_API_KEY@api.anthropic.com,github.com",
		"--on-secret-violation", "block-and-log",
	}
	if got := msbSecretArgs(o); !slices.Equal(got, want) {
		t.Errorf("msbSecretArgs = %q, want %q", got, want)
	}
	if j := strings.Join(msbCreateArgv(o, "n", "h"), " "); strings.Contains(j, "=k") {
		t.Errorf("create argv leaks the secret VALUE: %q", j)
	}
	if got := msbAuthEnvArgs(o); len(got) != 0 {
		t.Errorf("secrets mode must not also pass the value per exec: %q", got)
	}

	// With nothing to scope the secret to, the mode degrades to the default
	// rather than inventing a host list.
	noList := o
	noList.RT.Domains = nil
	if len(msbSecretArgs(noList)) != 0 || len(msbAuthEnvArgs(noList)) == 0 {
		t.Error("without an allowlist the secret mode must fall back to --env")
	}
}

// TestMSBRunArgv pins the one-shot argv: msb run has NO -i flag (stdin is piped
// by default), -t only on an interactive run with a real TTY, the command after
// the image and a `--` separator.
func TestMSBRunArgv(t *testing.T) {
	o := RunOpts{
		Interactive: true, Image: "img:1", Dest: "/d", Slug: "s",
		Args:  []string{"sh", "-c", "true"},
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{},
	}
	got := msbRunArgv(o)
	if slices.Contains(got, "-t") {
		t.Errorf("-t on non-TTY stdio: %q", got)
	}
	if slices.Contains(got, "-i") {
		t.Errorf("msbRunArgv = %q, must not carry -i (not in msb's grammar)", got)
	}
	tail := got[len(got)-5:]
	if !slices.Equal(tail, []string{"img:1", "--", "sh", "-c", "true"}) {
		t.Errorf("run argv tail = %q", tail)
	}
}

// TestMSBProfileJSONMount pins that a configured profile.json is shared at
// /run/sandboxer (via the per-sandbox run dir), and that staging copies it
// there.
func TestMSBProfileJSONMount(t *testing.T) {
	meta := t.TempDir()
	pj := filepath.Join(meta, "s.profile.json")
	if err := os.WriteFile(pj, []byte(`{"k":"v"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	o := RunOpts{Image: "img", Dest: "/d", Slug: "s", ProfileJSONPath: pj,
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}}
	runDir := filepath.Join(meta, "s.run")

	// The create argv mounts the run dir read-only at /run/sandboxer.
	if !strings.Contains(strings.Join(msbCreateArgv(o, "n", "h"), " "), runDir+":/run/sandboxer:ro") {
		t.Errorf("create argv missing profile.json mount: %q", msbCreateArgv(o, "n", "h"))
	}
	// Staging creates the dir and copies the file to profile.json.
	if err := stageProfileJSON(o); err != nil {
		t.Fatalf("stageProfileJSON: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(runDir, "profile.json"))
	if err != nil || string(got) != `{"k":"v"}` {
		t.Errorf("staged profile.json = %q, %v; want {\"k\":\"v\"}", got, err)
	}
	// No ProfileJSONPath → no mount, no-op stage.
	if strings.Contains(strings.Join(msbCreateArgv(RunOpts{Image: "i", Dest: "/d", Slug: "s"}, "n", "h"), " "), "/run/sandboxer") {
		t.Error("a sandbox with no profile.json must not mount /run/sandboxer")
	}
}

// TestMSBGuestExecArgv pins the guest-state primitive the shared session
// machinery (tmux capture, idleness) reaches through.
func TestMSBGuestExecArgv(t *testing.T) {
	want := []string{"exec", "n", "--", "tmux", "-L", "sandboxer", "list-sessions"}
	got := msbGuestExecArgv("n", []string{"tmux", "-L", "sandboxer", "list-sessions"})
	if !slices.Equal(got, want) {
		t.Errorf("msbGuestExecArgv = %q, want %q", got, want)
	}
	cmd := guestExec(msbEngine, "n", "tmux")
	if !strings.HasSuffix(cmd.Path, "msb") || !slices.Contains(cmd.Args, "exec") {
		t.Errorf("guestExec routed to %q %q", cmd.Path, cmd.Args)
	}
}

// TestMSBLifecycleArgv pins the tiny start/stop/remove/list builders — a
// rename of an msb subcommand would be caught here rather than at the first
// live enter.
func TestMSBLifecycleArgv(t *testing.T) {
	pairs := [][2][]string{
		{msbStartArgv("n"), {"start", "n"}},
		{msbStopArgv("n"), {"stop", "n"}},
		{msbRemoveArgv("n"), {"remove", "-f", "n"}},
		{msbListArgv(), {"list", "--format", "json"}},
		{msbGuestExecArgv("n", []string{"tmux"}), {"exec", "n", "--", "tmux"}},
	}
	for _, p := range pairs {
		if !slices.Equal(p[0], p[1]) {
			t.Errorf("argv = %q, want %q", p[0], p[1])
		}
	}
}

// TestMSBRemoveImage pins that removing a microsandbox image drops BOTH the
// runner's cached copy and the build tar it was imported from — an explicit
// `image rm` that left a multi-GB tar behind would be a surprise.
func TestMSBRemoveImage(t *testing.T) {
	t.Setenv("SANDBOXER_STATE", t.TempDir())
	setupFakeMSB(t) // its `image` verb exits non-zero: the image reads as absent
	if err := os.MkdirAll(vmImagesDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	tar := vmImagePath("img:1")
	if err := os.WriteFile(tar, []byte("tar"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveImage(msbEngine, "img:1"); err != nil {
		t.Fatalf("RemoveImage: %v", err)
	}
	if pathExists(tar) {
		t.Error("the build tar survived image rm")
	}
	if err := RemoveImage(msbEngine, "img:1"); err != nil {
		t.Errorf("image rm must be idempotent: %v", err)
	}
}

// TestParseMSBSandboxes pins the inventory decode, including the status
// vocabulary (msb capitalizes it) and the malformed-output stance.
func TestParseMSBSandboxes(t *testing.T) {
	got := parseMSBSandboxes([]byte(`[{"name":"a","status":"Running"},{"name":"b","status":"Stopped"}]`))
	want := []vmMachine{{Name: "a", State: "running"}, {Name: "b", State: "stopped"}}
	if !slices.Equal(got, want) {
		t.Errorf("parseMSBSandboxes = %+v, want %+v", got, want)
	}
	if got := parseMSBSandboxes([]byte("not json")); got != nil {
		t.Errorf("malformed output = %+v, want nil", got)
	}
}

// TestMSBHomeRoomy pins the MSB_HOME length guard — the prerequisite that is
// otherwise invisible until the first create fails on a too-long agent socket.
func TestMSBHomeRoomy(t *testing.T) {
	if !msbHomeRoomy("/home/dev/.msb") {
		t.Error("a short MSB_HOME must pass")
	}
	if msbHomeRoomy("") || msbHomeRoomy("/home/dev/"+strings.Repeat("deep/", 20)+".msb") {
		t.Error("an empty or very deep MSB_HOME must fail")
	}
	t.Setenv("MSB_HOME", "/tmp/msb")
	if msbHome() != "/tmp/msb" {
		t.Errorf("msbHome = %q, want the MSB_HOME override", msbHome())
	}
}

// fakeMSB is a bash stand-in for the msb CLI, shaped like the real one: one file
// per sandbox (content = status) so `list --format json` reflects the lifecycle,
// the image as the trailing positional on create, and the command after `--` on
// exec so exit codes propagate.
const fakeMSB = `#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = "--version" ]; then echo "msb 0.6.7-fake"; exit 0; fi
printf '%s\n' "$*" >> "$FAKE_LOG"
cmd="$1"; shift
D="$FAKE_MACHINES"; mkdir -p "$D"
case "$cmd" in
  list)
    out="["; first=1
    for f in "$D"/*; do
      [ -e "$f" ] || continue
      [ $first -eq 1 ] || out+=","
      first=0
      out+="{\"name\":\"$(basename "$f")\",\"status\":\"$(cat "$f")\"}"
    done
    printf '%s]\n' "$out"
    ;;
  create)
    name=""; a=("$@")
    for ((i=0; i<${#a[@]}; i++)); do
      if [ "${a[i]}" = "--name" ]; then name="${a[i+1]}"; fi
    done
    if [ -e "$D/$name" ]; then echo "sandbox $name already exists" >&2; exit 1; fi
    printf Running > "$D/$name"
    ;;
  start)  printf Running > "$D/${!#}" ;;
  stop)   printf Stopped > "$D/${!#}" ;;
  remove) for x in "$@"; do [ "$x" = "-f" ] || rm -f "$D/$x"; done ;;
  image)  [ "${1:-}" = remove ] || exit 1 ;;
  exec|run)
    argv=(); seen=0
    for x in "$@"; do
      if [ "$seen" = 1 ]; then argv+=("$x"); fi
      if [ "$x" = "--" ]; then seen=1; fi
    done
    "${argv[@]}"
    ;;
  *) echo "unknown $cmd" >&2; exit 3 ;;
esac
`

// setupFakeMSB installs the fake CLI and points the backend at it, returning the
// log path so a test can assert which subcommands ran.
func setupFakeMSB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "msb")
	if err := os.WriteFile(bin, []byte(fakeMSB), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "log")
	t.Setenv("SANDBOXER_MSB", bin)
	t.Setenv("SANDBOXER_STATE", filepath.Join(dir, "state"))
	t.Setenv("FAKE_LOG", log)
	t.Setenv("FAKE_MACHINES", filepath.Join(dir, "machines-live"))
	// The lifecycle under test starts at create; the ensure step's registry
	// pull (absent refs always "pull" against a fake store) is stubbed so no
	// test depends on the fake script growing a pull dialect.
	restorePull := msbPullImage
	msbPullImage = func(_, _ string, _ bool, _, _ io.Writer) error { return nil }
	t.Cleanup(func() { msbPullImage = restorePull })
	return log
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// TestMSBSessionLifecycle drives create → exec → reuse → recreate → stop →
// remove through the fake CLI over the session machinery, pinning the
// planSession dispatch, the record store, exit-code propagation — and that
// create already boots the machine (no extra start ever follows it).
func TestMSBSessionLifecycle(t *testing.T) {
	log := setupFakeMSB(t)
	base := t.TempDir()
	o := RunOpts{
		Engine: msbEngine, MountDest: true, Image: "img:1", Dest: "/d",
		Slug: "s", BaseDir: base, Stderr: &bytes.Buffer{},
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{},
	}
	name := SessionName("s", base)

	got, err := EnsureSession(o)
	if err != nil || got != name {
		t.Fatalf("EnsureSession create = %q, %v; want %q", got, err, name)
	}
	if rec := readVMRecord(name); rec.Hash != vmSessionWantHash(o) {
		t.Errorf("record hash = %q, want %q", rec.Hash, vmSessionWantHash(o))
	}
	if info := InspectSession(msbEngine, name); !info.Running {
		t.Error("machine not running after create (msb create boots it)")
	}
	if strings.Contains(readFile(t, log), "\nstart ") {
		t.Error("a start followed a create that already boots the machine")
	}
	// A booted machine gets its docker-compatible API socket brought up, so a
	// HEADLESS workload (exec, an agent) finds one — the rc.sh path only ever
	// covers interactive shells.
	if !strings.Contains(readFile(t, log), "exec "+name+" -- "+podmanSocketBin) {
		t.Errorf("create did not start the guest podman socket:\n%s", readFile(t, log))
	}

	logBefore := readFile(t, log)
	if got, err := EnsureSession(o); err != nil || got != name {
		t.Fatalf("EnsureSession reuse = %q, %v", got, err)
	}
	if strings.Count(readFile(t, log), "create ") != strings.Count(logBefore, "create ") {
		t.Error("a fresh running session was recreated instead of reused")
	}

	if code, _ := ExecSession(o, name, []string{"sh", "-c", "exit 7"}); code != 7 {
		t.Errorf("ExecSession exit = %d, want 7", code)
	}

	o2 := o
	o2.Mem = "2G"
	if _, err := EnsureSession(o2); err != nil {
		t.Fatalf("EnsureSession recreate: %v", err)
	}
	if !strings.Contains(readFile(t, log), "remove -f") {
		t.Error("stale session was not recreated (no remove)")
	}

	if err := StopSession(msbEngine, "s", base); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	states, err := SessionStates(msbEngine, base)
	if err != nil || states["s"] != "stopped" {
		t.Errorf("SessionStates = %v, %v; want s=stopped", states, err)
	}

	if err := RemoveSession(msbEngine, "s", base); err != nil {
		t.Fatalf("RemoveSession: %v", err)
	}
	if InspectSession(msbEngine, name).Exists {
		t.Error("machine still exists after remove")
	}
	if readVMRecord(name).Name != "" {
		t.Error("record survived remove")
	}
}

// TestMSBSessionStatesAndOrphans pins the record-vs-live cross reference: a
// recorded machine the engine forgot reads as "gone", a different base's
// session never leaks into a project view, and a record whose base dir
// vanished is an orphan — with the host-wide view grouping the same records by
// base dir, including the project whose dir is gone.
func TestMSBSessionStatesAndOrphans(t *testing.T) {
	t.Setenv("SANDBOXER_STATE", t.TempDir())
	base := t.TempDir()
	goneBase := filepath.Join(t.TempDir(), "deleted")

	writeRec := func(name, b, slug string) {
		if err := writeVMRecord(vmRecord{Name: name, BaseDir: b, Slug: slug, Hash: "h"}); err != nil {
			t.Fatal(err)
		}
	}
	writeRec("m-live", base, "live")
	writeRec("m-forgotten", base, "forgotten")
	writeRec("m-orphan", goneBase, "orphan")

	// Only m-live is in the live inventory.
	restore := msbListMachines
	msbListMachines = func() []vmMachine { return []vmMachine{{Name: "m-live", State: "running"}} }
	t.Cleanup(func() { msbListMachines = restore })

	states, err := SessionStates(msbEngine, base)
	if err != nil {
		t.Fatal(err)
	}
	if states["live"] != "running" {
		t.Errorf("live state = %q, want running", states["live"])
	}
	if states["forgotten"] != "gone" {
		t.Errorf("forgotten state = %q, want gone", states["forgotten"])
	}
	if _, ok := states["orphan"]; ok {
		t.Error("a different base's session leaked into states")
	}

	orphans, err := OrphanSessions(msbEngine)
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 1 || orphans[0] != "m-orphan" {
		t.Errorf("OrphanSessions = %v, want [m-orphan]", orphans)
	}

	all, err := AllSessionStates(msbEngine)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]string{
		base:     {"live": "running", "forgotten": "gone"},
		goneBase: {"orphan": "gone"},
	}
	if len(all) != len(want) {
		t.Fatalf("AllSessionStates = %v, want %v", all, want)
	}
	for b, states := range want {
		if !maps.Equal(all[b], states) {
			t.Errorf("AllSessionStates[%q] = %v, want %v", b, all[b], states)
		}
	}
}

// TestMSBInspectAbsent pins the zero SessionInfo for a machine the engine does
// not know.
func TestMSBInspectAbsent(t *testing.T) {
	restore := msbListMachines
	msbListMachines = func() []vmMachine { return nil }
	t.Cleanup(func() { msbListMachines = restore })
	if info := InspectSession(msbEngine, "nope"); info.Exists {
		t.Errorf("absent machine = %+v, want zero", info)
	}
}

// TestMSBRemoveAllSessions pins the base-scoped sweep: only the given base's
// machines are removed, another base's record is left intact.
func TestMSBRemoveAllSessions(t *testing.T) {
	setupFakeMSB(t)
	base := t.TempDir()
	other := t.TempDir()
	mkMachine := func(name, b, slug string) {
		if err := writeVMRecord(vmRecord{Name: name, BaseDir: b, Slug: slug, Hash: "h"}); err != nil {
			t.Fatal(err)
		}
		// Register it as live so the delete path runs.
		if err := os.WriteFile(filepath.Join(os.Getenv("FAKE_MACHINES"), name), []byte("Running"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(os.Getenv("FAKE_MACHINES"), 0o700); err != nil {
		t.Fatal(err)
	}
	mkMachine("m-a", base, "a")
	mkMachine("m-b", base, "b")
	mkMachine("m-keep", other, "keep")

	if err := RemoveAllSessions(msbEngine, base); err != nil {
		t.Fatalf("RemoveAllSessions: %v", err)
	}
	if readVMRecord("m-a").Name != "" || readVMRecord("m-b").Name != "" {
		t.Error("base machines' records survived RemoveAllSessions")
	}
	if readVMRecord("m-keep").Name == "" {
		t.Error("another base's record was swept")
	}
}

// TestRemoveSessionAnywhere: the teardown sweep covers the machine AND the
// host-side record — a record left by a hand-deleted machine is exactly the
// litter rm exists to sweep — and reports the engine a session was actually
// found on.
func TestRemoveSessionAnywhere(t *testing.T) {
	requireExec(t, "bash")
	base := t.TempDir()
	name := SessionName("s", base)

	t.Run("machine and record", func(t *testing.T) {
		setupFakeMSB(t)
		if err := os.MkdirAll(os.Getenv("FAKE_MACHINES"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := writeVMRecord(vmRecord{Name: name, BaseDir: base, Slug: "s", Hash: "h"}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(os.Getenv("FAKE_MACHINES"), name), []byte("Running"), 0o600); err != nil {
			t.Fatal(err)
		}
		removed, err := RemoveSessionAnywhere("s", base, config.Defaults{})
		if err != nil {
			t.Fatalf("RemoveSessionAnywhere: %v", err)
		}
		if !slices.Equal(removed, []string{msbEngine}) {
			t.Errorf("removed = %v, want [%s]", removed, msbEngine)
		}
		if _, ok := vmMachineByName(name); ok {
			t.Error("the machine survived the sweep")
		}
		if readVMRecord(name).Name != "" {
			t.Error("the machine record survived the sweep")
		}
	})

	t.Run("record alone still counts", func(t *testing.T) {
		setupFakeMSB(t)
		if err := writeVMRecord(vmRecord{Name: name, BaseDir: base, Slug: "s", Hash: "h"}); err != nil {
			t.Fatal(err)
		}
		removed, err := RemoveSessionAnywhere("s", base, config.Defaults{})
		if err != nil {
			t.Fatalf("RemoveSessionAnywhere: %v", err)
		}
		if !slices.Equal(removed, []string{msbEngine}) {
			t.Errorf("removed = %v, want [%s] — an orphaned record is still litter", removed, msbEngine)
		}
		if readVMRecord(name).Name != "" {
			t.Error("the orphaned record survived the sweep")
		}
	})
}

// TestMSBRunOneShot pins the ephemeral path (Run's microsandbox dispatch) and
// exit-code propagation.
func TestMSBRunOneShot(t *testing.T) {
	setupFakeMSB(t)
	o := RunOpts{
		Engine: msbEngine, Image: "img:1", Dest: "/d", Slug: "s",
		Args: []string{"sh", "-c", "exit 5"}, Stderr: &bytes.Buffer{},
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{},
	}
	code, err := Run(o)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 5 {
		t.Errorf("Run exit = %d, want 5", code)
	}
}

// TestMSBEngineResolution pins the engine identity: an installed msb resolves
// the microsandbox backend to its constant identity (never the override path),
// a missing one is a clear error and never a silent fallback to a container
// engine, and the sweeps include it so its machines cannot leak.
func TestMSBEngineResolution(t *testing.T) {
	setupFakeMSB(t)
	got, err := ResolveEngine("microsandbox", config.Defaults{})
	if err != nil || got != msbEngine {
		t.Fatalf("ResolveEngine(microsandbox) = %q, %v; want %q", got, err, msbEngine)
	}
	if !slices.Contains(SweepEngines(config.Defaults{}), msbEngine) {
		t.Error("SweepEngines omits microsandbox — its machines would leak")
	}
	present, version, _, _ := MsbStatus()
	if !present || version != "msb 0.6.7-fake" {
		t.Errorf("MsbStatus = %v, %q", present, version)
	}

	t.Setenv("SANDBOXER_MSB", "/nonexistent/msb-xyz")
	if _, err := ResolveEngine("microsandbox", config.Defaults{}); err == nil {
		t.Error("a missing msb must error, never fall back to a container engine")
	}
	if p, _, _, _ := MsbStatus(); p {
		t.Error("a missing msb must read as not present")
	}
	if engines := SweepEngines(config.Defaults{}); len(engines) != 0 {
		t.Errorf("SweepEngines with no msb = %v, want none", engines)
	}
}

// TestMSBEnsureImage pins the image resolution: an uncustomized ref — the
// prebuilt GHCR default and a user-set ref alike — passes through untouched
// (msb pulls and caches it host-side; no local build anywhere), while a
// customized profile's variant (non-empty spec, never published) is built once
// into the shared tar store and then imported into msb's own image store.
func TestMSBEnsureImage(t *testing.T) {
	t.Setenv("SANDBOXER_STATE", t.TempDir())
	restoreInspect, restoreLoad, restoreBuild := msbImageInspect, msbLoadImage, vmBuildImageToStore
	t.Cleanup(func() {
		msbImageInspect, msbLoadImage, vmBuildImageToStore = restoreInspect, restoreLoad, restoreBuild
	})

	cached := map[string]bool{}
	msbImageInspect = func(ref string) string {
		if cached[ref] {
			return "deadbeef"
		}
		return ""
	}
	var loaded string
	msbLoadImage = func(ref, tar string, _ io.Writer) error {
		if !pathExists(tar) {
			t.Errorf("load from a missing tar %q", tar)
		}
		loaded = ref
		cached[ref] = true
		return nil
	}
	built := 0
	vmBuildImageToStore = func(o RunOpts) error {
		built++
		return os.WriteFile(vmImagePath(o.Image), []byte("tar"), 0o600)
	}
	if err := os.MkdirAll(vmImagesDir(), 0o700); err != nil {
		t.Fatal(err)
	}

	restorePull := msbPullImage
	t.Cleanup(func() { msbPullImage = restorePull })
	pulled := 0
	forced := false
	msbPullImage = func(_, ref string, force bool, _, _ io.Writer) error {
		pulled++
		forced = forced || force
		cached[ref] = true
		return nil
	}
	// The create path pulls only what is MISSING, so it must never force: a
	// forced pull re-downloads unconditionally, which would turn every cold
	// create into a multi-GB fetch of an image the host may already be
	// getting for the first time anyway. Forcing belongs to `image pull`,
	// whose whole job is the refresh msb will otherwise no-op.
	t.Cleanup(func() {
		if forced {
			t.Error("msbEnsureImage forced a pull — that is the refresh command's job, not a cold create's")
		}
	})

	// An uncustomized ref ABSENT from msb's store is pulled EXPLICITLY first —
	// pull-on-create only exists on newer msb, so enter must not rely on it
	// (verified live: an older msb booted nothing until a manual pull).
	o := RunOpts{Engine: msbEngine, Image: "ghcr.io/x/y:1"}
	if ref, err := msbEnsureImage(o); err != nil || ref != o.Image || pulled != 1 {
		t.Errorf("an absent public ref must be pulled then passed through: %q, %v, pulled=%d", ref, err, pulled)
	}
	// Now cached: no second pull.
	if ref, err := msbEnsureImage(o); err != nil || ref != o.Image || pulled != 1 {
		t.Errorf("a cached ref must not re-pull: %q, %v, pulled=%d", ref, err, pulled)
	}

	// The DEFAULT ref is prebuilt and public too now: pulled when absent, never
	// a local autobuild.
	o.Image = config.DefaultImage
	if ref, err := msbEnsureImage(o); err != nil || ref != config.DefaultImage || pulled != 2 || built != 0 || loaded != "" {
		t.Errorf("the prebuilt default must pull, not build: ref=%q err=%v pulled=%d built=%d loaded=%q",
			ref, err, pulled, built, loaded)
	}

	// A flaky pull is RETRIED — msb resumes partial layers, so a second
	// attempt continues a multi-GB download instead of starting over.
	o.Image = "ghcr.io/x/flaky:1"
	attempts := 0
	msbPullImage = func(_, ref string, _ bool, _, _ io.Writer) error {
		attempts++
		if attempts == 1 {
			return errors.New("error decoding response body")
		}
		cached[ref] = true
		return nil
	}
	if ref, err := msbEnsureImage(o); err != nil || ref != o.Image || attempts != 2 {
		t.Errorf("a flaky pull must retry and succeed: %q, %v, attempts=%d", ref, err, attempts)
	}

	// A persistently failed pull is an actionable error naming the local-build
	// escape hatch, after the retries are exhausted.
	o.Image = "ghcr.io/x/absent:9"
	attempts = 0
	msbPullImage = func(_, _ string, _ bool, _, _ io.Writer) error { attempts++; return errors.New("net down") }
	if _, err := msbEnsureImage(o); err == nil || !strings.Contains(err.Error(), "sandboxer image build") || attempts != 3 {
		t.Errorf("a failed pull must retry 3x then hint the local build, got attempts=%d err=%v", attempts, err)
	}
	msbPullImage = func(_, ref string, _ bool, _, _ io.Writer) error { pulled++; cached[ref] = true; return nil }

	// A customized profile's variant is the one image still built locally.
	o.Spec = toolbox.Spec{Attrs: []string{"nixpkgs.ripgrep"}}
	o.Image = "sandboxer-toolbox:var-cafe01234567"
	ref, err := msbEnsureImage(o)
	if err != nil || ref != o.Image {
		t.Fatalf("msbEnsureImage = %q, %v", ref, err)
	}
	if built != 1 || loaded != o.Image {
		t.Errorf("build/load = %d/%q, want one build imported under the image name", built, loaded)
	}
	// Second call: cached in msb's store, imported from the tar that is there
	// now — no rebuild, no reimport.
	loaded = ""
	if _, err := msbEnsureImage(o); err != nil || built != 1 || loaded != "" {
		t.Errorf("a cached image was rebuilt or reimported (built=%d loaded=%q, err=%v)", built, loaded, err)
	}

	// A tar rebuilt behind msb's back (an `image build` of the same variant)
	// is RE-IMPORTED, not left stale — without this the reference is present
	// and nothing would ever refresh it.
	if err := os.WriteFile(vmImagePath(o.Image), []byte("rebuilt tar"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(vmImagePath(o.Image) + ".sha256")
	if _, err := msbEnsureImage(o); err != nil || built != 1 || loaded != o.Image {
		t.Errorf("a rebuilt tar was not reimported (built=%d loaded=%q, err=%v)", built, loaded, err)
	}
}

// TestPullImage pins the refresh path: `image pull` shells out to
// `msb pull <ref>` with the runner's output streamed through, and a failed
// pull surfaces as an error naming the command — never a silent no-op.
func TestPullImage(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "msb")
	log := filepath.Join(dir, "log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$FAKE_PULL_LOG\"\necho progress\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SANDBOXER_MSB", bin)
	t.Setenv("FAKE_PULL_LOG", log)

	var out bytes.Buffer
	if err := PullImage(msbEngine, "ghcr.io/x/toolbox:latest", true, &out, io.Discard); err != nil {
		t.Fatalf("PullImage: %v", err)
	}
	if got := readFile(t, log); !strings.Contains(got, "pull -f ghcr.io/x/toolbox:latest") {
		t.Errorf("msb argv = %q, want `pull -f <ref>` (a refresh msb cannot no-op)", got)
	}
	if !strings.Contains(out.String(), "progress") {
		t.Errorf("stdout = %q, want the runner's progress streamed through", out.String())
	}

	t.Setenv("SANDBOXER_MSB", "/nonexistent/msb-xyz")
	if err := PullImage(msbEngine, "img:1", true, io.Discard, io.Discard); err == nil ||
		!strings.Contains(err.Error(), "msb pull img:1") {
		t.Errorf("failed pull = %v, want an error naming the command", err)
	}
}

// TestVMCreateFailurePullHint pins the offline-host diagnostic: the stock
// image is prebuilt and PULLED by msb at create time, so a failed create with
// the default ref absent from msb's store hints at the network (or the local
// build escape hatch) — while a user-set ref, or a default already cached,
// surfaces the raw error alone.
func TestVMCreateFailurePullHint(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "msb")
	script := `#!/bin/sh
case "$1" in
  list) echo "[]" ;;
  image) exit 1 ;;
  create) echo "pull failed" >&2; exit 1 ;;
  *) exit 0 ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SANDBOXER_MSB", bin)
	t.Setenv("SANDBOXER_STATE", filepath.Join(dir, "state"))

	o := RunOpts{
		Engine: msbEngine, Image: config.DefaultImage, Dest: "/var/tmp/box",
		Slug: "s", BaseDir: t.TempDir(), Stderr: &bytes.Buffer{},
	}
	_, err := EnsureSession(o)
	if err == nil || !strings.Contains(err.Error(), "network needed to pull the prebuilt image") ||
		!strings.Contains(err.Error(), "sandboxer image build") {
		t.Errorf("default-image create failure = %v, want the pull/build hint", err)
	}

	// A user-set ref (a profile's image.ref) is exactly as pullable as the
	// default — the same hint applies when it is absent from the store.
	o.Image = "example.com/custom:1"
	if _, err := EnsureSession(o); err == nil || !strings.Contains(err.Error(), "network needed") {
		t.Errorf("custom-ref create failure = %v, want the pull/build hint", err)
	}

	// A var- variant is never pulled — it fails for its own build reasons.
	if hint := msbCreateFailHint("sandboxer-toolbox:var-cafe01234567"); hint != "" {
		t.Errorf("variant hint = %q, want none", hint)
	}

	// A default already in msb's store did not fail on the pull either.
	restore := msbImageInspect
	msbImageInspect = func(string) string { return "deadbeef" }
	t.Cleanup(func() { msbImageInspect = restore })
	if hint := msbCreateFailHint(config.DefaultImage); hint != "" {
		t.Errorf("cached default hint = %q, want none", hint)
	}
}

// TestMSBGuestProxyURL pins the guest URL and the wall's door rule: a
// host-loopback proxy becomes the host.microsandbox.internal alias (msb's
// guest loopback is guest-local) with an allow@host door; a remote proxy
// passes through with a name-bound door on its own host and port (domain= for
// a single-label name); a v6 literal or garbage yields no door.
func TestMSBGuestProxyURL(t *testing.T) {
	tests := []struct {
		raw, url, rule string
	}{
		{"http://127.0.0.1:8888", "http://host.microsandbox.internal:8888", "allow@host:tcp:8888"},
		{"http://localhost:3128", "http://host.microsandbox.internal:3128", "allow@host:tcp:3128"},
		{"http://[::1]:3128", "http://host.microsandbox.internal:3128", "allow@host:tcp:3128"},
		{"http://127.1.2.3:80", "http://host.microsandbox.internal:80", "allow@host:tcp:80"},
		{"http://localhost", "http://host.microsandbox.internal", "allow@host:tcp"},
		{"http://proxy.corp.example:3128", "http://proxy.corp.example:3128", "allow@proxy.corp.example:tcp:3128"},
		{"http://proxybox:3128", "http://proxybox:3128", "allow@domain=proxybox:tcp:3128"},
		{"http://10.0.0.5:3128", "http://10.0.0.5:3128", "allow@10.0.0.5:tcp:3128"},
		{"http://[2001:db8::1]:3128", "http://[2001:db8::1]:3128", ""},
		{"not a url", "not a url", ""},
	}
	for _, tt := range tests {
		gotURL, gotRule := msbGuestProxyURL(tt.raw)
		if gotURL != tt.url || gotRule != tt.rule {
			t.Errorf("msbGuestProxyURL(%q) = %q, %q; want %q, %q", tt.raw, gotURL, gotRule, tt.url, tt.rule)
		}
	}
}

// TestMSBLoadStoredImageGzip pins the store-tar handoff format: the stored
// artifact is nix's buildLayeredImage output — a GZIPPED docker tarball — and
// msb's load reads the outer archive raw, so the import must hand msb an
// uncompressed temp copy (made beside the store tar, never /tmp) and remove it
// afterwards. A plain tar passes through under its own store path.
func TestMSBLoadStoredImageGzip(t *testing.T) {
	t.Setenv("SANDBOXER_STATE", t.TempDir())
	restoreLoad := msbLoadImage
	t.Cleanup(func() { msbLoadImage = restoreLoad })
	if err := os.MkdirAll(vmImagesDir(), 0o700); err != nil {
		t.Fatal(err)
	}

	var gzTar bytes.Buffer
	zw := gzip.NewWriter(&gzTar)
	if _, err := zw.Write([]byte("docker-save tar bytes")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	store := vmImagePath("img:1")
	if err := os.WriteFile(store, gzTar.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	var loadedTar string
	msbLoadImage = func(_, tar string, _ io.Writer) error {
		loadedTar = tar
		data, err := os.ReadFile(tar)
		if err != nil {
			t.Errorf("read the tar handed to msb load: %v", err)
		}
		if string(data) != "docker-save tar bytes" {
			t.Errorf("msb load got %q, want the DECOMPRESSED tar", data)
		}
		return nil
	}
	if err := msbLoadStoredImage("img:1", nil); err != nil {
		t.Fatalf("msbLoadStoredImage: %v", err)
	}
	if loadedTar == store {
		t.Error("a gzipped store tar was handed to msb load raw")
	}
	if filepath.Dir(loadedTar) != vmImagesDir() {
		t.Errorf("temp tar %q not beside the store (avoid tmpfs /tmp)", loadedTar)
	}
	if pathExists(loadedTar) {
		t.Errorf("temp tar %q left behind after the import", loadedTar)
	}

	// A plain (uncompressed) tar is handed over under its own store path.
	if err := os.WriteFile(store, []byte("plain tar"), 0o600); err != nil {
		t.Fatal(err)
	}
	msbLoadImage = func(_, tar string, _ io.Writer) error { loadedTar = tar; return nil }
	if err := msbLoadStoredImage("img:1", nil); err != nil {
		t.Fatalf("msbLoadStoredImage(plain): %v", err)
	}
	if loadedTar != store {
		t.Errorf("a plain tar was copied to %q, want the store path handed over as-is", loadedTar)
	}

	// A gzip magic with a corrupt body is an error, not a truncated import.
	if err := os.WriteFile(store, []byte{0x1f, 0x8b, 0xff, 0xff}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := msbLoadStoredImage("img:1", nil); err == nil {
		t.Error("a corrupt gzipped tar must fail the import")
	}
}

// TestBuildVMImageMSB pins the explicit build entry point: the tar is built
// into the shared store AND imported into msb's own image store — which is
// what its create reads, so skipping the import would build an image enter
// never sees.
func TestBuildVMImageMSB(t *testing.T) {
	setupFakeMSB(t)
	restoreLoad, restoreBuild := msbLoadImage, vmBuildImageToStore
	t.Cleanup(func() { msbLoadImage, vmBuildImageToStore = restoreLoad, restoreBuild })

	if err := os.MkdirAll(vmImagesDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	vmBuildImageToStore = func(o RunOpts) error {
		return os.WriteFile(vmImagePath(o.Image), []byte("tar"), 0o600)
	}
	var loaded string
	msbLoadImage = func(ref, _ string, _ io.Writer) error { loaded = ref; return nil }

	if err := BuildVMImage(msbEngine, "img:1", toolbox.Spec{}, nil); err != nil {
		t.Fatalf("BuildVMImage: %v", err)
	}
	if loaded != "img:1" {
		t.Errorf("built image imported as %q, want img:1", loaded)
	}

	// A missing runner is an error up front, never a silent no-op.
	t.Setenv("SANDBOXER_MSB", "/nonexistent/msb-xyz")
	if err := BuildVMImage(msbEngine, "img:1", toolbox.Spec{}, nil); err == nil {
		t.Error("BuildVMImage must error when msb is absent")
	}
}

// TestMSBEnsureImageNoAutobuild pins the fail-fast: with autobuild off, a
// missing VARIANT image (the one kind still built locally) names the
// microsandbox build command, not the microvm one.
func TestMSBEnsureImageNoAutobuild(t *testing.T) {
	t.Setenv("SANDBOXER_STATE", t.TempDir())
	t.Setenv("SANDBOXER_NO_AUTOBUILD", "1")
	restore := msbImageInspect
	msbImageInspect = func(string) string { return "" }
	t.Cleanup(func() { msbImageInspect = restore })

	_, err := msbEnsureImage(RunOpts{Engine: msbEngine, Image: "sandboxer-toolbox:var-cafe01234567",
		Spec: toolbox.Spec{Attrs: []string{"nixpkgs.ripgrep"}}})
	if err == nil || !strings.Contains(err.Error(), "--backend microsandbox") {
		t.Errorf("error = %v, want a microsandbox build hint", err)
	}
}

// TestMSBImageIDPrefersStoreTar pins the freshness authority for a store-built
// image: the build tar's content id, not msb's own cached digest. The cached
// digest only moves after msbEnsureImage re-imports the tar — inside
// create/recreate, AFTER planSession already ruled — so comparing it against
// the record compared the old copy with itself: a rebuilt image never read as
// stale and a live machine kept its old rootfs forever (`image build` looked
// like a no-op).
func TestMSBImageIDPrefersStoreTar(t *testing.T) {
	t.Setenv("SANDBOXER_STATE", t.TempDir())
	restore := msbImageInspect
	msbImageInspect = func(string) string { return "cached-digest" }
	t.Cleanup(func() { msbImageInspect = restore })

	name := "sandboxer-toolbox:latest"
	// No store tar (a public/pulled ref): msb's cached copy is the only id.
	if got := msbImageID(name); got != "cached-digest" {
		t.Fatalf("imageID with no store tar = %q, want the msb digest", got)
	}

	p := vmImagePath(name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("build-1"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := msbImageID(name)
	if len(first) != 64 {
		t.Fatalf("imageID with a store tar = %q, want the tar's 64-hex id", first)
	}
	// A REBUILT tar (new bytes, fresh sidecar — vmStoreImage rewrites both)
	// must flip the id even though msb's cached digest is unchanged.
	if err := os.Remove(p + ".sha256"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("build-2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if second := msbImageID(name); second == first {
		t.Fatal("rebuilt tar kept the old image id — a live machine would never read stale")
	}
}

// TestMSBPortArgs pins the two halves a published port turns into: the runner's
// forward in the shared block, and — while the wall is up — the ONE ingress
// rule that lets the forwarded connection actually reach the guest. Verified
// against msb 0.6.7: --no-net denies BOTH directions, so the forward alone
// binds on the host and the dial dies as a reset inside.
func TestMSBPortArgs(t *testing.T) {
	ports := []config.Port{
		{Bind: "127.0.0.1", Host: 8080, Guest: 3080, Proto: "tcp"},
		{Bind: "0.0.0.0", Host: 5353, Guest: 53, Proto: "udp"},
	}
	o := RunOpts{
		Image: "img:1", Dest: "/d", Slug: "s",
		RT:    config.Runtime{Egress: true, Domains: []string{"github.com"}, Ports: ports},
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{},
	}
	if got, want := msbPortArgs(o), []string{
		"-p", "127.0.0.1:8080:3080",
		"-p", "0.0.0.0:5353:53/udp",
	}; !slices.Equal(got, want) {
		t.Errorf("msbPortArgs =\n%q\nwant\n%q", got, want)
	}
	if got, want := msbNetworkArgs(o), []string{
		"--no-net",
		"--net-rule", "allow@*.github.com:tcp:80,allow@*.github.com:tcp:443",
		"--net-rule", "allow:ingress@0.0.0.0/0:tcp:3080",
		"--net-rule", "allow:ingress@0.0.0.0/0:udp:53",
	}; !slices.Equal(got, want) {
		t.Errorf("msbNetworkArgs (walled) =\n%q\nwant\n%q", got, want)
	}
	// The forwards ride in the shared block, so they fold into the session
	// hash: publishing or moving a port recreates the machine.
	if j := strings.Join(msbCommonArgs(o), " "); !strings.Contains(j, "-p 127.0.0.1:8080:3080") {
		t.Errorf("msbCommonArgs must carry the forwards: %q", j)
	}
	unpublished := o
	unpublished.RT.Ports = nil
	if vmSessionWantHash(o) == vmSessionWantHash(unpublished) {
		t.Error("publishing a port did not flip the session hash")
	}
}

// TestMSBPortArgsOpenNetwork: with no wall there is nothing to open — msb
// leaves ingress at allow when no ingress rule is set, which is exactly why a
// published port works on an open network with no rule at all. Emitting one
// anyway would be the dangerous kind of no-op: an explicit rule replaces the
// implicit open default, so a lone ingress rule would silently deny egress.
func TestMSBPortArgsOpenNetwork(t *testing.T) {
	o := RunOpts{
		RT:    config.Runtime{Egress: false, Ports: []config.Port{{Bind: "127.0.0.1", Host: 8080, Guest: 3080, Proto: "tcp"}}},
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{},
	}
	if got := msbNetworkArgs(o); got != nil {
		t.Errorf("msbNetworkArgs (open) = %q, want none", got)
	}
	if got, want := msbPortArgs(o), []string{"-p", "127.0.0.1:8080:3080"}; !slices.Equal(got, want) {
		t.Errorf("msbPortArgs = %q, want %q", got, want)
	}
}

// TestMSBPortArgsProxyWall: the combined wall gets its ingress doors too, after
// the allowlist and the proxy's own door.
func TestMSBPortArgsProxyWall(t *testing.T) {
	o := RunOpts{
		RT: config.Runtime{
			Egress: true, Proxy: "http://127.0.0.1:8888",
			Ports: []config.Port{{Bind: "127.0.0.1", Host: 3080, Guest: 3080, Proto: "tcp"}},
		},
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{},
	}
	got := msbNetworkArgs(o)
	if len(got) < 2 || !slices.Equal(got[len(got)-2:], []string{"--net-rule", "allow:ingress@0.0.0.0/0:tcp:3080"}) {
		t.Errorf("msbNetworkArgs (proxy wall) = %q, want the ingress door last", got)
	}
	if !slices.Contains(got, "allow@host:tcp:8888") {
		t.Errorf("msbNetworkArgs (proxy wall) lost the proxy door: %q", got)
	}
}

// TestMSBPortsPreflight: a host port someone else already holds is caught
// BEFORE the machine is built, naming the address — msb's own failure arrives
// late and names only a bind error.
func TestMSBPortsPreflight(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot bind a probe listener: %v", err)
	}
	defer ln.Close()
	taken := ln.Addr().(*net.TCPAddr).Port

	o := RunOpts{
		Image: "img:1", Dest: "/d", Slug: "s", HomeDir: "/d/.home",
		RT:    config.Runtime{Ports: []config.Port{{Bind: "127.0.0.1", Host: taken, Guest: 3080, Proto: "tcp"}}},
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{},
	}
	err = msbPreflight(o)
	if err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("msbPreflight = %v, want the taken host port reported", err)
	}

	// A free port passes, and a UDP forward is not probed at all (a TCP bind
	// says nothing about it).
	free := o
	free.RT.Ports = []config.Port{{Bind: "127.0.0.1", Host: taken, Guest: 53, Proto: "udp"}}
	if err := msbPreflight(free); err != nil {
		t.Fatalf("msbPreflight (udp) = %v, want nil", err)
	}
}
