package backend

import (
	"bytes"
	"compress/gzip"
	"io"
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

	// The two runners must never share a hash for the same options: their argv
	// dialects differ, so a backend switch has to read as stale.
	smol := o
	smol.Engine = smolvmEngine
	if vmSessionWantHash(smol) == base {
		t.Error("smolvm and microsandbox produced the same session hash")
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
			name: "proxy delegates egress",
			o:    RunOpts{RT: config.Runtime{Egress: true, Proxy: "http://p:8080", NoProxy: "localhost"}},
			want: []string{
				"-e", "HTTP_PROXY=http://p:8080", "-e", "http_proxy=http://p:8080",
				"-e", "HTTPS_PROXY=http://p:8080", "-e", "https_proxy=http://p:8080",
				"-e", "NO_PROXY=localhost", "-e", "no_proxy=localhost",
			},
		},
		{
			// The guest's 127.0.0.1 is its own stack, so a loopback proxy is
			// rewritten to msb's host alias AND the host group is opened on the
			// proxy port — with `public` restated, since any explicit --net
			// replaces the implicit open default.
			name: "loopback proxy is rewritten to the host alias",
			o:    RunOpts{RT: config.Runtime{Egress: true, Proxy: "http://127.0.0.1:8888"}},
			want: []string{
				"-e", "HTTP_PROXY=http://host.microsandbox.internal:8888",
				"-e", "http_proxy=http://host.microsandbox.internal:8888",
				"-e", "HTTPS_PROXY=http://host.microsandbox.internal:8888",
				"-e", "https_proxy=http://host.microsandbox.internal:8888",
				"--net", "public", "--net-rule", "allow@host:tcp:8888",
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

// TestMSBNetTargetsKeepSubdomains pins the property the smolvm runner cannot
// offer: an allowlist entry covers the domain AND its subdomains, exactly as the
// squid sidecar's leading-dot dstdomain does.
func TestMSBNetTargetsKeepSubdomains(t *testing.T) {
	got := msbNetTargets([]string{"cloudfront.net", ".github.com"})
	want := []string{"*.cloudfront.net", "*.github.com"}
	if !slices.Equal(got, want) {
		t.Errorf("msbNetTargets = %q, want %q", got, want)
	}
	// The smolvm translation of the same list narrows to exact hosts — the
	// regression this backend exists to avoid.
	if slices.Equal(vmAllowHosts([]string{"cloudfront.net"}), got) {
		t.Error("expected the two runners' allowlist translations to differ")
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

// TestVMRunnerDialects pins that the two runners really are two vocabularies
// for the same verbs — a rename of a subcommand in either would be caught here
// rather than at the first live enter.
func TestVMRunnerDialects(t *testing.T) {
	smol, msb := vmRunnerFor(smolvmEngine), vmRunnerFor(msbEngine)
	if _, ok := vmRunnerFor("").(smolvmRunner); !ok {
		t.Error("an unknown engine must keep the original smolvm behaviour")
	}
	if !isVMEngine(smolvmEngine) || !isVMEngine(msbEngine) || isVMEngine("docker") {
		t.Error("isVMEngine mis-classified an engine")
	}
	if smol.startsOnCreate() || !msb.startsOnCreate() {
		t.Error("startsOnCreate: smolvm needs a start, microsandbox does not")
	}
	if smol.recordDir() != "" || msb.recordDir() != msbEngine {
		t.Errorf("record dirs = %q / %q", smol.recordDir(), msb.recordDir())
	}
	pairs := [][2][]string{
		{smol.startArgv("n"), {"machine", "start", "--name", "n"}},
		{smol.stopArgv("n"), {"machine", "stop", "--name", "n"}},
		{smol.removeArgv("n"), {"machine", "delete", "--name", "n", "-f"}},
		{smol.guestExecArgv("n", []string{"tmux"}), {"machine", "exec", "--name", "n", "--", "tmux"}},
		{msb.startArgv("n"), {"start", "n"}},
		{msb.stopArgv("n"), {"stop", "n"}},
		{msb.removeArgv("n"), {"remove", "-f", "n"}},
		{msb.guestExecArgv("n", []string{"tmux"}), {"exec", "n", "--", "tmux"}},
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

// TestMSBRecordsAreRunnerScoped pins that the two runners' machine records never
// collide: the same sandbox name recorded under both keeps two records, so
// switching backends cannot strand the old runner's machine.
func TestMSBRecordsAreRunnerScoped(t *testing.T) {
	t.Setenv("SANDBOXER_STATE", t.TempDir())
	base := t.TempDir()
	rec := vmRecord{Name: "sandboxer-s-1", BaseDir: base, Slug: "s", Hash: "h"}
	if err := writeVMRecord(smolvmRunner{}, rec); err != nil {
		t.Fatal(err)
	}
	msbRec := rec
	msbRec.Hash = "h2"
	if err := writeVMRecord(msbRunner{}, msbRec); err != nil {
		t.Fatal(err)
	}
	if got := readVMRecord(smolvmRunner{}, rec.Name); got.Hash != "h" {
		t.Errorf("smolvm record = %+v, want hash h", got)
	}
	if got := readVMRecord(msbRunner{}, rec.Name); got.Hash != "h2" {
		t.Errorf("microsandbox record = %+v, want hash h2", got)
	}
	if recs := listVMRecords(smolvmRunner{}); len(recs) != 1 {
		t.Errorf("smolvm sweep saw %d records, want its own 1", len(recs))
	}
	removeVMRecord(msbRunner{}, rec.Name)
	if got := readVMRecord(smolvmRunner{}, rec.Name); got.Hash != "h" {
		t.Error("removing the microsandbox record dropped the smolvm one")
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
	return log
}

// TestMSBSessionLifecycle drives create → exec → reuse → recreate → stop →
// remove through the fake CLI over the SHARED session machinery, pinning that a
// microsandbox session converges by the same policy as a smolvm one — including
// the runner difference that create already boots the machine (no extra start).
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
	if rec := readVMRecord(msbRunner{}, name); rec.Hash != vmSessionWantHash(o) {
		t.Errorf("record hash = %q, want %q", rec.Hash, vmSessionWantHash(o))
	}
	if info := InspectSession(msbEngine, name); !info.Running {
		t.Error("machine not running after create (msb create boots it)")
	}
	if strings.Contains(readFile(t, log), "\nstart ") {
		t.Error("a start followed a create that already boots the machine")
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
	if readVMRecord(msbRunner{}, name).Name != "" {
		t.Error("record survived remove")
	}
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
	if _, err := ResolveEngine("microsandbox", config.Defaults{Engine: "docker"}); err == nil {
		t.Error("a missing msb must error, never fall back to a container engine")
	}
	if p, _, _, _ := MsbStatus(); p {
		t.Error("a missing msb must read as not present")
	}
}

// TestMSBEnsureImage pins the image resolution: a custom public ref passes
// through untouched, and the locally-built toolbox image is built once into the
// shared tar store and then imported into msb's own image store.
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

	o := RunOpts{Engine: msbEngine, Image: "ghcr.io/x/y:1"}
	if ref, err := msbEnsureImage(o); err != nil || ref != o.Image {
		t.Errorf("a public ref must pass through: %q, %v", ref, err)
	}

	o.Image = config.DefaultImage
	ref, err := msbEnsureImage(o)
	if err != nil || ref != config.DefaultImage {
		t.Fatalf("msbEnsureImage = %q, %v", ref, err)
	}
	if built != 1 || loaded != config.DefaultImage {
		t.Errorf("build/load = %d/%q, want one build imported under the image name", built, loaded)
	}
	// Second call: cached in msb's store, imported from the tar that is there
	// now — no rebuild, no reimport.
	loaded = ""
	if _, err := msbEnsureImage(o); err != nil || built != 1 || loaded != "" {
		t.Errorf("a cached image was rebuilt or reimported (built=%d loaded=%q, err=%v)", built, loaded, err)
	}

	// A tar rebuilt behind msb's back (e.g. `image build --backend microvm`,
	// which shares the same artifact) is RE-IMPORTED, not left stale — without
	// this the reference is present and nothing would ever refresh it.
	if err := os.WriteFile(vmImagePath(o.Image), []byte("rebuilt tar"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(vmImagePath(o.Image) + ".sha256")
	if _, err := msbEnsureImage(o); err != nil || built != 1 || loaded != config.DefaultImage {
		t.Errorf("a rebuilt tar was not reimported (built=%d loaded=%q, err=%v)", built, loaded, err)
	}
}

// TestMSBGuestProxyURL pins the loopback rewrite: msb's guest loopback is
// guest-local (no TSI passthrough), so a host-loopback proxy must become the
// host.microsandbox.internal alias plus an allow@host rule on its port, while
// any other proxy passes through with no rule (allow@public covers it).
func TestMSBGuestProxyURL(t *testing.T) {
	tests := []struct {
		raw, url, rule string
	}{
		{"http://127.0.0.1:8888", "http://host.microsandbox.internal:8888", "allow@host:tcp:8888"},
		{"http://localhost:3128", "http://host.microsandbox.internal:3128", "allow@host:tcp:3128"},
		{"http://[::1]:3128", "http://host.microsandbox.internal:3128", "allow@host:tcp:3128"},
		{"http://127.1.2.3:80", "http://host.microsandbox.internal:80", "allow@host:tcp:80"},
		{"http://localhost", "http://host.microsandbox.internal", "allow@host:tcp"},
		{"http://proxy.corp:3128", "http://proxy.corp:3128", ""},
		{"http://10.0.0.5:3128", "http://10.0.0.5:3128", ""},
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

// TestBuildVMImageMSB pins the explicit build entry point for the microsandbox
// backend: both runners share ONE build artifact (the tar), and only
// microsandbox additionally imports it into its own image store — which is what
// its create reads, so skipping that step would build an image enter never sees.
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

	// smolvm builds the same tar and imports nothing.
	setupFakeSmolvm(t)
	if err := os.MkdirAll(vmImagesDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	loaded = ""
	if err := BuildVMImage(smolvmEngine, "img:1", toolbox.Spec{}, nil); err != nil {
		t.Fatalf("BuildVMImage(smolvm): %v", err)
	}
	if loaded != "" {
		t.Errorf("the smolvm build imported %q into the microsandbox store", loaded)
	}

	// A missing runner is an error up front, never a silent no-op.
	t.Setenv("SANDBOXER_MSB", "/nonexistent/msb-xyz")
	if err := BuildVMImage(msbEngine, "img:1", toolbox.Spec{}, nil); err == nil {
		t.Error("BuildVMImage must error when msb is absent")
	}
}

// TestMSBEnsureImageNoAutobuild pins the fail-fast: with autobuild off, a
// missing image names the microsandbox build command, not the microvm one.
func TestMSBEnsureImageNoAutobuild(t *testing.T) {
	t.Setenv("SANDBOXER_STATE", t.TempDir())
	t.Setenv("SANDBOXER_NO_AUTOBUILD", "1")
	restore := msbImageInspect
	msbImageInspect = func(string) string { return "" }
	t.Cleanup(func() { msbImageInspect = restore })

	_, err := msbEnsureImage(RunOpts{Engine: msbEngine, Image: config.DefaultImage})
	if err == nil || !strings.Contains(err.Error(), "--backend microsandbox") {
		t.Errorf("error = %v, want a microsandbox build hint", err)
	}
}
