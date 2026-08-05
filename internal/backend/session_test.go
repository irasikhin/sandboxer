package backend

import (
	"bytes"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
)

func TestSessionName(t *testing.T) {
	// Sanitization: every rune outside [a-zA-Z0-9_.-] becomes '-'.
	for slug, want := range map[string]string{
		"my-box":    "my-box",
		"a b":       "a-b",
		"x/y:z":     "x-y-z",
		"ok_Name.1": "ok_Name.1",
		"héllo":     "h-llo",
		"":          "",
	} {
		name := SessionName(slug, "/base")
		prefix, suffix := "sandboxer-"+want+"-", name[strings.LastIndex(name, "-")+1:]
		if !strings.HasPrefix(name, prefix) {
			t.Errorf("SessionName(%q) = %q, want prefix %q", slug, name, prefix)
		}
		// The suffix is exactly the first 8 hex chars of sha256(baseDir).
		if len(suffix) != 8 || strings.Trim(suffix, "0123456789abcdef") != "" {
			t.Errorf("SessionName(%q) suffix = %q, want 8 hex chars", slug, suffix)
		}
	}

	// Deterministic: same inputs, same name.
	if a, b := SessionName("s", "/p/.sandboxer"), SessionName("s", "/p/.sandboxer"); a != b {
		t.Errorf("SessionName not stable: %q vs %q", a, b)
	}
	// Different baseDir → different name (same slug, different project).
	if a, b := SessionName("s", "/p1/.sandboxer"), SessionName("s", "/p2/.sandboxer"); a == b {
		t.Errorf("SessionName identical across base dirs: %q", a)
	}
}

// TestCreateArgv pins the exact create argv: detached + named + labeled, the
// shared commonArgs block, then the image and the keep-alive command — and
// deliberately NO --rm, no -i/-t and no o.Args.
func TestCreateArgv(t *testing.T) {
	o := RunOpts{
		MountDest: true, Engine: "podman", Image: "img:1", Dest: "/d", Slug: "s", BaseDir: "/b",
		HomeDir: "/d/.home", Mem: "2G", CPU: "150%", Interactive: true,
		Args:  []string{"bash", "-l"},
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{},
	}
	got := CreateArgv(o, "sandboxer-s-deadbeef", "abc123")
	userns := strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
	want := []string{
		"run", "-d", "--init", "--name", "sandboxer-s-deadbeef",
		"--label", "sandboxer.managed=true",
		"--label", "sandboxer.slug=s",
		"--label", "sandboxer.base=/b",
		"--label", "sandboxer.hash=abc123",
		"--label", "sandboxer.mounts=",
		"--user", userns, "--cap-drop=ALL", "--security-opt", "no-new-privileges",
		"--workdir", "/d", "--volume", "/d:/d:rw",
		"--env", "SANDBOXER_IN_CONTAINER=1",
		"--env", "SANDBOXER_SLUG=s", "--env", "SANDBOXER_SANDBOX_DIR=/d",
		"--env", "LANG=C.UTF-8",
		"--env", "HOME=/d/.home", "--volume", "/d/.home:/d/.home:rw",
		"--userns=keep-id",
		"--add-host=host.docker.internal:host-gateway",
		"--add-host=host.containers.internal:host-gateway",
		"--memory", "2G", "--cpus", "1.5",
		"img:1", "sleep", "infinity",
	}
	if !slices.Equal(got, want) {
		t.Errorf("CreateArgv =\n%q\nwant\n%q", got, want)
	}
}

// TestExecArgv pins the exact exec argv, with and without a host TERM. A
// non-TTY stdin/stdout (test buffers) must not produce -t.
func TestExecArgv(t *testing.T) {
	o := RunOpts{Dest: "/d", Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}}

	t.Setenv("TERM", "xterm-256color")
	got := ExecArgv(o, "n", []string{"claude", "--continue"})
	want := []string{"exec", "-i", "-w", "/d", "--env", "TERM=xterm-256color", "n", "claude", "--continue"}
	if !slices.Equal(got, want) {
		t.Errorf("ExecArgv with TERM =\n%q\nwant\n%q", got, want)
	}

	t.Setenv("TERM", "")
	got = ExecArgv(o, "n", []string{"sh"})
	want = []string{"exec", "-i", "-w", "/d", "n", "sh"}
	if !slices.Equal(got, want) {
		t.Errorf("ExecArgv without TERM =\n%q\nwant\n%q", got, want)
	}
}

// TestAuthEnvIsProcessScoped pins where credentials may appear: on the exec
// that starts a shell (so every shell gets the CURRENT host token, with no
// rebuild), never on the session container's create argv — which would both
// park the token in a long-lived container's environment and, through
// ConfigHash, make a rotation look like a changed profile.
func TestAuthEnvIsProcessScoped(t *testing.T) {
	o := RunOpts{
		MountDest: true, Engine: "docker", Image: "img:1", Dest: "/d", Slug: "s", BaseDir: "/b",
		AuthEnv: []string{"ANTHROPIC_API_KEY=k", "CLAUDE_CODE_OAUTH_TOKEN=t"},
		Stdin:   strings.NewReader(""), Stdout: &bytes.Buffer{},
	}
	t.Setenv("TERM", "")

	create := strings.Join(CreateArgv(o, "n", "h"), " ")
	for _, kv := range o.AuthEnv {
		if strings.Contains(create, kv) {
			t.Errorf("create argv carries %q — the session container must hold no credential", kv)
		}
	}

	got := ExecArgv(o, "n", []string{"sh"})
	want := []string{"exec", "-i", "-w", "/d",
		"--env", "ANTHROPIC_API_KEY=k", "--env", "CLAUDE_CODE_OAUTH_TOKEN=t", "n", "sh"}
	if !slices.Equal(got, want) {
		t.Errorf("ExecArgv =\n%q\nwant\n%q", got, want)
	}

	// A one-shot run still bakes them: there the agent IS the main process,
	// and the container dies with it. The profile's env comes later in the
	// argv, so it keeps overriding per key (last --env wins).
	run, err := RunArgv(o)
	if err != nil {
		t.Fatal(err)
	}
	authAt, profileEnvAt := slices.Index(run, "ANTHROPIC_API_KEY=k"), slices.Index(run, "LANG=C.UTF-8")
	if authAt < 0 || authAt > profileEnvAt {
		t.Errorf("run argv must carry the auth env, ahead of the profile's own:\n%q", run)
	}
}

func TestConfigHash(t *testing.T) {
	base := RunOpts{
		MountDest: true, Engine: "docker", Image: "img:1", Dest: "/d", Slug: "s", BaseDir: "/b",
		Profile: &config.Profile{Env: map[string]string{"B": "2", "A": "1"}},
	}
	h := ConfigHash(base, "", "")
	if len(h) != 64 || strings.Trim(h, "0123456789abcdef") != "" {
		t.Fatalf("ConfigHash = %q, want 64 hex chars", h)
	}
	// Stable across calls (multi-key profile env exercises the sorted order).
	for i := 0; i < 5; i++ {
		if g := ConfigHash(base, "", ""); g != h {
			t.Fatalf("ConfigHash unstable: %q vs %q", g, h)
		}
	}

	// Name/labels are excluded: BaseDir feeds only the name + labels, so it may
	// not flip the hash.
	same := []struct {
		desc string
		o    RunOpts
	}{
		{"different BaseDir", func() RunOpts { o := base; o.BaseDir = "/elsewhere"; return o }()},
		// Auth env is scoped to the process, not the container, so it is not in
		// the create argv at all. This is load-bearing: a hash that tracked a
		// token value went stale on every rotation AND in every terminal that
		// did not export the var, which made a session permanently stale from
		// ambient shell state. Each exec carries the current value instead.
		{"auth env present", func() RunOpts {
			o := base
			o.AuthEnv = []string{"CLAUDE_CODE_OAUTH_TOKEN=t1"}
			return o
		}()},
		{"auth env rotated", func() RunOpts {
			o := base
			o.AuthEnv = []string{"CLAUDE_CODE_OAUTH_TOKEN=t2"}
			return o
		}()},
		// Mount IDs are stamped as a LABEL only — MountGen already carries the
		// same identity into the hash. If this ever leaked into commonArgs it
		// would double-count, and worse: every session in existence would read
		// as stale the moment the field shipped, which is precisely the
		// mass-rebuild the label was designed to avoid.
		{"mount IDs", func() RunOpts { o := base; o.MountIDs = "cGF0aAAxCg"; return o }()},
	}
	for _, tc := range same {
		if g := ConfigHash(tc.o, "", ""); g != h {
			t.Errorf("%s changed the hash: %q vs %q", tc.desc, g, h)
		}
	}

	// Any real config change flips it.
	diff := []struct {
		desc string
		o    RunOpts
		eg   [2]string
	}{
		{"image", func() RunOpts { o := base; o.Image = "img:2"; return o }(), [2]string{"", ""}},
		{"memory limit", func() RunOpts { o := base; o.Mem = "2G"; return o }(), [2]string{"", ""}},
		{"profile env", func() RunOpts {
			o := base
			o.Profile = &config.Profile{Env: map[string]string{"B": "2", "A": "changed"}}
			return o
		}(), [2]string{"", ""}},
		{"extra mount", func() RunOpts {
			o := base
			o.Profile = &config.Profile{
				Env:         map[string]string{"B": "2", "A": "1"},
				ExtraMounts: []config.Mount{{Source: "/s", Target: "/t"}},
			}
			return o
		}(), [2]string{"", ""}},
		{"egress network", base, [2]string{"net", "http://proxy"}},
		// A bumped sandbox-dir generation must invalidate the session: its bind
		// mounts hold the pre-deletion directory (see RunOpts.DestGen).
		{"dest generation", func() RunOpts { o := base; o.DestGen = "2"; return o }(), [2]string{"", ""}},
	}
	for _, tc := range diff {
		if g := ConfigHash(tc.o, tc.eg[0], tc.eg[1]); g == h {
			t.Errorf("%s did not change the hash: %q", tc.desc, g)
		}
	}

	// With egress active (egNet set) the squid.conf fingerprint is folded in, so
	// changing the routes flips the hash even though the create argv is identical.
	withRoute := base
	withRoute.RT.Routes = []config.Route{{Domains: []string{"a.com"}, Proxy: "http://bypass:8080"}}
	if ConfigHash(base, "net", "http://p") == ConfigHash(withRoute, "net", "http://p") {
		t.Error("with egress active, changing routes must change the hash (squid.conf fingerprint)")
	}
	// The fingerprint is egress-only: with egNet="" (compose) the routes make no
	// difference — that path stays deliberately fingerprint-free.
	if ConfigHash(base, "", "") != ConfigHash(withRoute, "", "") {
		t.Error("with no egress network the routes must not affect the hash")
	}
}

// --- lifecycle ---------------------------------------------------------------

// sessionEngine writes an executable engine stub for the lifecycle tests.
// Every invocation's argv is appended to the returned log; behavior branches
// on the engine subcommand ($1) and is steered through env vars:
//
//	SBX_FAIL_ON          any invocation whose subcommand matches exits 1
//	SBX_INSPECT_FAIL     `container inspect` of the session container exits 1
//	SBX_INSPECT_FAIL_ONCE a marker-file path: only the FIRST session inspect
//	                     exits 1 (the marker records that it happened)
//	SBX_INSPECT_OUT      stdout of `container inspect` (session or batched)
//	SBX_PROXY_RUNNING    `container inspect <…-proxy>` prints "true" when "1",
//	                     exits 1 (proxy missing) otherwise
//	SBX_EXEC_OUT         stdout of `exec` (the tmux list-clients idleness probe)
//	SBX_PANES_OUT        stdout of the `exec … list-panes` layout capture
//	SBX_PSEO_OUT         stdout of the `exec … ps -eo` agent-detection listing;
//	                     SBX_PSEO_FAIL makes it exit 1
//	SBX_IMAGE_ID         stdout of `image inspect` (the ImageID probe)
//	SBX_PS_OUT           stdout of `ps`; SBX_PS_FAIL makes `ps` exit 1
func sessionEngine(t *testing.T) (engine, logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	engine = filepath.Join(dir, "engine")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "` + logPath + `"
[ -n "$SBX_FAIL_ON" ] && [ "$1" = "$SBX_FAIL_ON" ] && { [ -n "$SBX_STDERR" ] && printf '%s\n' "$SBX_STDERR" >&2; exit 1; }
for a in "$@"; do last="$a"; done
case "$1" in
container)
  case "$last" in
  *-proxy)
    [ "$SBX_PROXY_RUNNING" = "1" ] && { echo true; exit 0; }
    exit 1 ;;
  *)
    if [ -n "$SBX_INSPECT_FAIL_ONCE" ] && [ ! -f "$SBX_INSPECT_FAIL_ONCE" ]; then
      : > "$SBX_INSPECT_FAIL_ONCE"; exit 1
    fi
    [ -n "$SBX_INSPECT_FAIL" ] && exit 1
    printf '%s\n' "$SBX_INSPECT_OUT" ;;
  esac ;;
exec)
  case "$*" in
  *list-panes*) printf '%s\n' "$SBX_PANES_OUT" ;;
  *"ps -eo"*)
    [ -n "$SBX_PSEO_FAIL" ] && exit 1
    printf '%s\n' "$SBX_PSEO_OUT" ;;
  *) printf '%s\n' "$SBX_EXEC_OUT" ;;
  esac ;;
image) printf '%s\n' "$SBX_IMAGE_ID" ;;
ps)
  [ -n "$SBX_PS_FAIL" ] && exit 1
  printf '%s\n' "$SBX_PS_OUT" ;;
esac
exit 0
`
	if err := os.WriteFile(engine, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return engine, logPath
}

// engineLog returns the stub's invocations, one argv per line (nil before the
// first call).
func engineLog(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func hasLine(lines []string, exact string) bool { return slices.Contains(lines, exact) }

func findPrefixLine(lines []string, prefix string) string {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	return ""
}

// TestPlanSession pins the full pure policy table: every combination of
// exists/running/hash-freshness/idleness maps to exactly one action.
func TestPlanSession(t *testing.T) {
	const want = "h-want"
	hash := func(fresh bool) string {
		if fresh {
			return want
		}
		return "h-stale"
	}
	rows := []struct {
		exists, running, fresh, idle bool
		action                       sessionAction
	}{
		// No container: create, whatever the other dimensions claim.
		{false, false, false, false, actCreate},
		{false, false, false, true, actCreate},
		{false, false, true, false, actCreate},
		{false, false, true, true, actCreate},
		{false, true, false, false, actCreate},
		{false, true, false, true, actCreate},
		{false, true, true, false, actCreate},
		{false, true, true, true, actCreate},
		// Stopped: start when fresh, recreate when stale.
		{true, false, false, false, actRecreate},
		{true, false, false, true, actRecreate},
		{true, false, true, false, actStart},
		{true, false, true, true, actStart},
		// Running: exec when fresh; stale always recreates (sandboxer tracks
		// no in-container clients — multiplexing is the user's own business).
		{true, true, false, false, actRecreate},
		{true, true, false, true, actRecreate},
		{true, true, true, false, actExec},
		{true, true, true, true, actExec},
	}
	for _, r := range rows {
		info := SessionInfo{Exists: r.exists, Running: r.running, Hash: hash(r.fresh)}
		if got := planSession(info, want, ""); got != r.action {
			t.Errorf("planSession(exists=%v running=%v fresh=%v) = %d, want %d",
				r.exists, r.running, r.fresh, got, r.action)
		}
	}
	// A missing hash label ("" — e.g. a container from an older version) is
	// stale, never fresh.
	if got := planSession(SessionInfo{Exists: true, Running: false}, want, ""); got != actRecreate {
		t.Errorf("missing hash label = %d, want actRecreate", got)
	}
}

// TestPlanSessionImageStaleness pins the image-ID half of freshness: a
// hash-fresh container whose image was rebuilt under the same tag (the
// recorded ID no longer matches the engine's current one) is stale — same
// decision table as a profile change — while an unknown ID on either side
// skips the check, and the comparison tolerates docker's "sha256:" prefix.
func TestPlanSessionImageStaleness(t *testing.T) {
	const want = "h-want"
	rows := []struct {
		desc           string
		got, wantImage string
		running        bool
		action         sessionAction
	}{
		{"running rebuilt", "old", "new", true, actRecreate},
		{"stopped rebuilt", "old", "new", false, actRecreate},
		{"container ID unknown", "", "new", true, actExec},
		{"image absent locally", "old", "", true, actExec},
		{"prefix-tolerant match", "sha256:abc", "abc", true, actExec},
	}
	for _, r := range rows {
		info := SessionInfo{Exists: true, Running: r.running, Hash: want, ImageID: r.got}
		if got := planSession(info, want, r.wantImage); got != r.action {
			t.Errorf("%s: planSession = %d, want %d", r.desc, got, r.action)
		}
	}
	// A stopped container with a matching hash and image simply starts.
	info := SessionInfo{Exists: true, Hash: want, ImageID: "abc"}
	if got := planSession(info, want, "sha256:abc"); got != actStart {
		t.Errorf("stopped fresh-by-image = %d, want actStart", got)
	}
	// A stale hash dominates: the image matching cannot make a session fresh.
	info = SessionInfo{Exists: true, Running: true, Hash: "h-stale", ImageID: "abc"}
	if got := planSession(info, want, "abc"); got != actRecreate {
		t.Errorf("stale hash with a fresh image = %d, want actRecreate", got)
	}
}

// TestImageFresh pins the pure comparison: empty on either side skips, and the
// "sha256:" prefix never causes a false stale across engines.
func TestImageFresh(t *testing.T) {
	for _, r := range []struct {
		got, want string
		fresh     bool
	}{
		{"", "", true},
		{"", "abc", true},
		{"abc", "", true},
		{"abc", "abc", true},
		{"sha256:abc", "abc", true},
		{"abc", "sha256:abc", true},
		{"sha256:abc", "sha256:abc", true},
		{"abc", "def", false},
		{"sha256:abc", "sha256:def", false},
	} {
		if got := ImageFresh(r.got, r.want); got != r.fresh {
			t.Errorf("ImageFresh(%q, %q) = %v, want %v", r.got, r.want, got, r.fresh)
		}
	}
}

// TestStaleReason: a hash mismatch reads as a profile change; a hash-fresh
// session that still went stale can only mean the image was rebuilt.
func TestStaleReason(t *testing.T) {
	if got := staleReason(SessionInfo{Hash: "h"}, "h"); got != "image rebuilt" {
		t.Errorf("staleReason(fresh hash) = %q, want %q", got, "image rebuilt")
	}
	if got := staleReason(SessionInfo{Hash: "old"}, "h"); got != "profile changed" {
		t.Errorf("staleReason(stale hash) = %q, want %q", got, "profile changed")
	}
}

func TestImageID(t *testing.T) {
	requireExec(t, "sh")
	engine, logPath := sessionEngine(t)

	// Docker prefixes the ID with "sha256:", podman does not — both normalize.
	for out, want := range map[string]string{
		"sha256:0123abcd": "0123abcd",
		"0123abcd":        "0123abcd",
		"":                "",
	} {
		t.Setenv("SBX_IMAGE_ID", out)
		if got := ImageID(engine, "img:1"); got != want {
			t.Errorf("ImageID(%q) = %q, want %q", out, got, want)
		}
	}

	// An absent image (non-zero inspect) is unknown, never an error.
	t.Setenv("SBX_FAIL_ON", "image")
	if got := ImageID(engine, "img:1"); got != "" {
		t.Errorf("missing image: ImageID = %q, want \"\"", got)
	}

	want := "image inspect --format {{.Id}} img:1"
	if lines := engineLog(t, logPath); !hasLine(lines, want) {
		t.Errorf("engine log missing %q:\n%s", want, strings.Join(lines, "\n"))
	}
}

func TestInspectSession(t *testing.T) {
	requireExec(t, "sh")
	engine, logPath := sessionEngine(t)

	t.Setenv("SBX_INSPECT_FAIL", "1")
	if got := InspectSession(engine, "n"); got != (SessionInfo{}) {
		t.Errorf("missing container: InspectSession = %+v, want zero", got)
	}

	t.Setenv("SBX_INSPECT_FAIL", "")
	for out, want := range map[string]SessionInfo{
		"true abc123 sha256:i1": {Exists: true, Running: true, Hash: "abc123", ImageID: "i1"},
		"false abc123 i1":       {Exists: true, Running: false, Hash: "abc123", ImageID: "i1"},
		// A missing hash label is an EMPTY middle field — the image ID must
		// not shift into the hash slot.
		"true  sha256:i1": {Exists: true, Running: true, ImageID: "i1"},
		"true abc123":     {Exists: true, Running: true, Hash: "abc123"}, // image field absent
		"true":            {Exists: true, Running: true},                 // hash + image absent
		"":                {Exists: true},                                // unparseable output tolerated
		// The mounts label rides LAST. It is base64url by construction, so it
		// can never contain a space and never shifts a field; absent (the
		// trailing separator eaten by TrimSpace) it simply is not read.
		"true abc123 sha256:i1 QUJD": {Exists: true, Running: true, Hash: "abc123", ImageID: "i1", Mounts: "QUJD"},
		"true  i1 QUJD":              {Exists: true, Running: true, ImageID: "i1", Mounts: "QUJD"},
	} {
		t.Setenv("SBX_INSPECT_OUT", out)
		if got := InspectSession(engine, "n"); got != want {
			t.Errorf("InspectSession(%q) = %+v, want %+v", out, got, want)
		}
	}

	// One inspect call per InspectSession, reading state + hash label + image
	// ID + mounts label together.
	want := `container inspect --format {{.State.Running}} {{index .Config.Labels "sandboxer.hash"}} {{.Image}} {{index .Config.Labels "sandboxer.mounts"}} n`
	if lines := engineLog(t, logPath); !hasLine(lines, want) {
		t.Errorf("engine log missing %q:\n%s", want, strings.Join(lines, "\n"))
	}
}

// sessionOpts is the no-egress baseline the EnsureSession scenarios tweak.
func sessionOpts(engine string) RunOpts {
	return RunOpts{
		MountDest: true, Engine: engine, Image: "img:1", Dest: "/d", Slug: "s", BaseDir: "/b",
		RT: config.Runtime{}, NoEgress: true,
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
	}
}

func TestEnsureSessionCreate(t *testing.T) {
	requireExec(t, "sh")
	engine, logPath := sessionEngine(t)
	o := sessionOpts(engine)
	name, hash := SessionName("s", "/b"), ConfigHash(o, "", "")

	t.Setenv("SBX_INSPECT_FAIL", "1") // not found
	got, err := EnsureSession(o)
	if err != nil || got != name {
		t.Fatalf("EnsureSession = (%q, %v), want (%q, nil)", got, err, name)
	}
	lines := engineLog(t, logPath)
	run := findPrefixLine(lines, "run -d --init --name "+name)
	if run == "" {
		t.Fatalf("no create invocation:\n%s", strings.Join(lines, "\n"))
	}
	for _, want := range []string{"--label sandboxer.hash=" + hash, " img:1 sleep infinity"} {
		if !strings.Contains(run, want) {
			t.Errorf("create argv missing %q:\n%s", want, run)
		}
	}
	if findPrefixLine(lines, "start ") != "" {
		t.Errorf("fresh create should not start anything:\n%s", strings.Join(lines, "\n"))
	}
	// Without egress, leftovers from a previous egress-enabled life are swept.
	if !hasLine(lines, "rm -f "+name+"-proxy") {
		t.Errorf("create did not sweep stale egress:\n%s", strings.Join(lines, "\n"))
	}
}

func TestEnsureSessionAdoptRunningFresh(t *testing.T) {
	requireExec(t, "sh")
	engine, logPath := sessionEngine(t)
	o := sessionOpts(engine)
	name, hash := SessionName("s", "/b"), ConfigHash(o, "", "")

	t.Setenv("SBX_INSPECT_OUT", "true "+hash)
	got, err := EnsureSession(o)
	if err != nil || got != name {
		t.Fatalf("EnsureSession = (%q, %v), want (%q, nil)", got, err, name)
	}
	lines := engineLog(t, logPath)
	for _, banned := range []string{"run -d", "start ", "rm -f", "exec "} {
		if findPrefixLine(lines, banned) != "" {
			t.Errorf("adopting a fresh running session must not %q:\n%s", banned, strings.Join(lines, "\n"))
		}
	}
}

func TestEnsureSessionStartStoppedFresh(t *testing.T) {
	requireExec(t, "sh")
	engine, logPath := sessionEngine(t)
	o := sessionOpts(engine)
	name, hash := SessionName("s", "/b"), ConfigHash(o, "", "")

	t.Setenv("SBX_INSPECT_OUT", "false "+hash)
	if got, err := EnsureSession(o); err != nil || got != name {
		t.Fatalf("EnsureSession = (%q, %v), want (%q, nil)", got, err, name)
	}
	lines := engineLog(t, logPath)
	if !hasLine(lines, "start "+name) {
		t.Errorf("stopped+fresh should start the container:\n%s", strings.Join(lines, "\n"))
	}
	if findPrefixLine(lines, "run -d") != "" {
		t.Errorf("stopped+fresh must not create:\n%s", strings.Join(lines, "\n"))
	}

	// A failing start surfaces as an error — carrying the engine's own
	// diagnostic, not a bare exit status the user cannot act on.
	t.Setenv("SBX_FAIL_ON", "start")
	t.Setenv("SBX_STDERR", "Error: cannot set up namespace")
	_, err := EnsureSession(o)
	if err == nil || !strings.Contains(err.Error(), "start session") {
		t.Errorf("start failure = %v, want a start session error", err)
	}
	if err != nil && !strings.Contains(err.Error(), "cannot set up namespace") {
		t.Errorf("start failure dropped the engine's stderr: %v", err)
	}
}

func TestEnsureSessionRecreateStale(t *testing.T) {
	requireExec(t, "sh")

	t.Run("stopped", func(t *testing.T) {
		engine, logPath := sessionEngine(t)
		o := sessionOpts(engine)
		name := SessionName("s", "/b")
		stderr := &bytes.Buffer{}
		o.Stderr = stderr

		t.Setenv("SBX_INSPECT_OUT", "false deadbeef")
		if got, err := EnsureSession(o); err != nil || got != name {
			t.Fatalf("EnsureSession = (%q, %v), want (%q, nil)", got, err, name)
		}
		if !strings.Contains(stderr.String(), "recreating session: profile changed") {
			t.Errorf("missing recreate notice, stderr = %q", stderr.String())
		}
		lines := engineLog(t, logPath)
		if !hasLine(lines, "rm -f "+name) {
			t.Errorf("stale session was not removed:\n%s", strings.Join(lines, "\n"))
		}
		if findPrefixLine(lines, "run -d --init") == "" {
			t.Errorf("stale session was not recreated:\n%s", strings.Join(lines, "\n"))
		}
	})

	t.Run("running", func(t *testing.T) {
		engine, logPath := sessionEngine(t)
		o := sessionOpts(engine)
		name := SessionName("s", "/b")

		t.Setenv("SBX_INSPECT_OUT", "true deadbeef")
		if got, err := EnsureSession(o); err != nil || got != name {
			t.Fatalf("EnsureSession = (%q, %v), want (%q, nil)", got, err, name)
		}
		lines := engineLog(t, logPath)
		if !hasLine(lines, "rm -f "+name) || findPrefixLine(lines, "run -d --init") == "" {
			t.Errorf("running stale session was not replaced:\n%s", strings.Join(lines, "\n"))
		}
	})

	t.Run("rm failure surfaces", func(t *testing.T) {
		engine, _ := sessionEngine(t)
		o := sessionOpts(engine)

		t.Setenv("SBX_INSPECT_OUT", "false deadbeef")
		t.Setenv("SBX_FAIL_ON", "rm")
		if _, err := EnsureSession(o); err == nil || !strings.Contains(err.Error(), "remove stale session") {
			t.Errorf("rm failure = %v, want a remove stale session error", err)
		}
	})
}

// TestEnsureSessionImageRebuilt: a hash-fresh session whose container runs an
// older build of the image (rebuilt under the same tag — the engine now holds
// a different ID) is stale: recreated when idle, refused when busy, and the
// notice says "image rebuilt", not "profile changed". A matching ID (modulo
// the "sha256:" prefix) is adopted untouched.
func TestEnsureSessionImageRebuilt(t *testing.T) {
	requireExec(t, "sh")

	t.Run("running and idle recreates", func(t *testing.T) {
		engine, logPath := sessionEngine(t)
		o := sessionOpts(engine)
		name, hash := SessionName("s", "/b"), ConfigHash(o, "", "")
		stderr := &bytes.Buffer{}
		o.Stderr = stderr

		t.Setenv("SBX_IMAGE_ID", "sha256:newimg")           // the engine's current image…
		t.Setenv("SBX_INSPECT_OUT", "true "+hash+" oldimg") // …but the container runs the old build
		t.Setenv("SBX_EXEC_OUT", "")                        // no tmux clients → idle
		if got, err := EnsureSession(o); err != nil || got != name {
			t.Fatalf("EnsureSession = (%q, %v), want (%q, nil)", got, err, name)
		}
		if !strings.Contains(stderr.String(), "recreating session: image rebuilt") {
			t.Errorf("missing image-rebuilt notice, stderr = %q", stderr.String())
		}
		lines := engineLog(t, logPath)
		if !hasLine(lines, "rm -f "+name) || findPrefixLine(lines, "run -d --init") == "" {
			t.Errorf("image-stale session was not replaced:\n%s", strings.Join(lines, "\n"))
		}
	})

	t.Run("matching image adopts", func(t *testing.T) {
		engine, logPath := sessionEngine(t)
		o := sessionOpts(engine)
		name, hash := SessionName("s", "/b"), ConfigHash(o, "", "")

		t.Setenv("SBX_IMAGE_ID", "sha256:img1") // docker prefixes…
		t.Setenv("SBX_INSPECT_OUT", "true "+hash+" img1")
		if got, err := EnsureSession(o); err != nil || got != name {
			t.Fatalf("EnsureSession = (%q, %v), want (%q, nil)", got, err, name)
		}
		lines := engineLog(t, logPath)
		for _, banned := range []string{"run -d", "start ", "rm -f", "exec "} {
			if findPrefixLine(lines, banned) != "" {
				t.Errorf("adopting a fresh session must not %q:\n%s", banned, strings.Join(lines, "\n"))
			}
		}
	})
}

// egressOpts is the egress-enabled variant; its hash is computed with the
// session's stable egress identifiers, exactly as EnsureSession does.
func egressOpts(t *testing.T, engine string) (o RunOpts, name, hash string) {
	o = sessionOpts(engine)
	o.NoEgress = false
	o.RT = config.Runtime{Egress: true, Domains: []string{"x.com"}}
	// A writable BaseDir: the egress sidecar writes its generated squid.conf
	// there (it doubles as the session's state dir at run time).
	o.BaseDir = t.TempDir()
	name = SessionName("s", o.BaseDir)
	hash = ConfigHash(o, name+"-int", "http://"+name+"-proxy:8888")
	return o, name, hash
}

func TestEnsureSessionEgress(t *testing.T) {
	requireExec(t, "sh")

	t.Run("create brings up the named sidecar first", func(t *testing.T) {
		engine, logPath := sessionEngine(t)
		o, name, hash := egressOpts(t, engine)

		t.Setenv("SBX_INSPECT_FAIL", "1") // not found
		if got, err := EnsureSession(o); err != nil || got != name {
			t.Fatalf("EnsureSession = (%q, %v), want (%q, nil)", got, err, name)
		}
		lines := engineLog(t, logPath)
		if !hasLine(lines, "network create --internal "+name+"-int") {
			t.Errorf("missing stable-named internal network:\n%s", strings.Join(lines, "\n"))
		}
		if findPrefixLine(lines, "run -d --name "+name+"-proxy") == "" {
			t.Errorf("missing stable-named proxy sidecar:\n%s", strings.Join(lines, "\n"))
		}
		run := findPrefixLine(lines, "run -d --init --name "+name)
		if run == "" {
			t.Fatalf("no session create:\n%s", strings.Join(lines, "\n"))
		}
		for _, want := range []string{
			"--network " + name + "-int",
			"HTTP_PROXY=http://" + name + "-proxy:8888",
			"--label sandboxer.hash=" + hash,
		} {
			if !strings.Contains(run, want) {
				t.Errorf("session create argv missing %q:\n%s", want, run)
			}
		}
	})

	t.Run("adopt with a healthy proxy", func(t *testing.T) {
		engine, logPath := sessionEngine(t)
		o, name, hash := egressOpts(t, engine)

		t.Setenv("SBX_INSPECT_OUT", "true "+hash)
		t.Setenv("SBX_PROXY_RUNNING", "1")
		if got, err := EnsureSession(o); err != nil || got != name {
			t.Fatalf("EnsureSession = (%q, %v), want (%q, nil)", got, err, name)
		}
		lines := engineLog(t, logPath)
		if findPrefixLine(lines, "run -d") != "" || findPrefixLine(lines, "rm -f") != "" {
			t.Errorf("healthy adoption must not recreate:\n%s", strings.Join(lines, "\n"))
		}
	})

	t.Run("dead proxy forces recreate", func(t *testing.T) {
		engine, logPath := sessionEngine(t)
		o, name, hash := egressOpts(t, engine)
		stderr := &bytes.Buffer{}
		o.Stderr = stderr

		t.Setenv("SBX_INSPECT_OUT", "true "+hash) // fresh container…
		// …but SBX_PROXY_RUNNING is unset: the proxy inspect fails (missing).
		if got, err := EnsureSession(o); err != nil || got != name {
			t.Fatalf("EnsureSession = (%q, %v), want (%q, nil)", got, err, name)
		}
		if !strings.Contains(stderr.String(), "egress proxy is gone") {
			t.Errorf("missing proxy-gone notice, stderr = %q", stderr.String())
		}
		lines := engineLog(t, logPath)
		if !hasLine(lines, "rm -f "+name) || findPrefixLine(lines, "run -d --init") == "" {
			t.Errorf("dead proxy should rebuild the session:\n%s", strings.Join(lines, "\n"))
		}
	})

	t.Run("stopped fresh restarts the proxy too", func(t *testing.T) {
		engine, logPath := sessionEngine(t)
		o, name, hash := egressOpts(t, engine)

		t.Setenv("SBX_INSPECT_OUT", "false "+hash)
		if got, err := EnsureSession(o); err != nil || got != name {
			t.Fatalf("EnsureSession = (%q, %v), want (%q, nil)", got, err, name)
		}
		lines := engineLog(t, logPath)
		for _, want := range []string{"start " + name + "-proxy", "start " + name} {
			if !hasLine(lines, want) {
				t.Errorf("missing %q:\n%s", want, strings.Join(lines, "\n"))
			}
		}
	})

	t.Run("unstartable proxy forces recreate", func(t *testing.T) {
		engine, logPath := sessionEngine(t)
		o, name, hash := egressOpts(t, engine)

		t.Setenv("SBX_INSPECT_OUT", "false "+hash)
		t.Setenv("SBX_FAIL_ON", "start") // proxy start fails → rebuild both
		if got, err := EnsureSession(o); err != nil || got != name {
			t.Fatalf("EnsureSession = (%q, %v), want (%q, nil)", got, err, name)
		}
		lines := engineLog(t, logPath)
		if !hasLine(lines, "rm -f "+name) || findPrefixLine(lines, "run -d --init") == "" {
			t.Errorf("unstartable proxy should rebuild the session:\n%s", strings.Join(lines, "\n"))
		}
	})

	t.Run("empty allowlist fails before any engine call", func(t *testing.T) {
		engine, logPath := sessionEngine(t)
		o, _, _ := egressOpts(t, engine)
		o.RT.Domains = nil

		if _, err := EnsureSession(o); !errors.Is(err, errEmptyAllowlist) {
			t.Fatalf("EnsureSession = %v, want errEmptyAllowlist", err)
		}
		if lines := engineLog(t, logPath); lines != nil {
			t.Errorf("engine was invoked despite the misconfiguration:\n%s", strings.Join(lines, "\n"))
		}
	})

	t.Run("sidecar failure fails closed", func(t *testing.T) {
		engine, _ := sessionEngine(t)
		o, _, _ := egressOpts(t, engine)

		t.Setenv("SBX_INSPECT_FAIL", "1")
		t.Setenv("SBX_FAIL_ON", "network") // network create fails
		_, err := EnsureSession(o)
		if err == nil || !strings.Contains(err.Error(), "refusing to run on an open network") {
			t.Errorf("sidecar failure = %v, want a fail-closed egress error", err)
		}
	})
}

func TestEnsureSessionCreateFailure(t *testing.T) {
	requireExec(t, "sh")

	t.Run("create error surfaces", func(t *testing.T) {
		engine, _ := sessionEngine(t)
		o := sessionOpts(engine)

		t.Setenv("SBX_INSPECT_FAIL", "1")
		t.Setenv("SBX_FAIL_ON", "run")
		if _, err := EnsureSession(o); err == nil || !strings.Contains(err.Error(), "create session") {
			t.Errorf("create failure = %v, want a create session error", err)
		}
	})

	t.Run("duplicate-name race adopts the winner", func(t *testing.T) {
		engine, logPath := sessionEngine(t)
		o := sessionOpts(engine)
		name, hash := SessionName("s", "/b"), ConfigHash(o, "", "")

		// First inspect: not found (so we take the create path); our create
		// then loses the name race; the re-inspect sees the concurrent
		// winner's container running and configuration-fresh — adopt it.
		t.Setenv("SBX_INSPECT_FAIL_ONCE", filepath.Join(t.TempDir(), "marker"))
		t.Setenv("SBX_INSPECT_OUT", "true "+hash)
		t.Setenv("SBX_FAIL_ON", "run")
		got, err := EnsureSession(o)
		if err != nil || got != name {
			t.Fatalf("EnsureSession = (%q, %v), want (%q, nil)", got, err, name)
		}
		if lines := engineLog(t, logPath); findPrefixLine(lines, "run -d --init") == "" {
			t.Errorf("the create must have been attempted:\n%s", strings.Join(lines, "\n"))
		}
	})

	t.Run("missing image with autobuild disabled fails with a hint", func(t *testing.T) {
		engine, _ := sessionEngine(t)
		o := sessionOpts(engine)
		o.Image = config.DefaultImage // a locally-built image, never pullable

		t.Setenv("SBX_INSPECT_FAIL", "1") // not found → create path
		t.Setenv("SBX_FAIL_ON", "image")  // the `image inspect` probe: absent
		t.Setenv("SANDBOXER_NO_AUTOBUILD", "1")
		if _, err := EnsureSession(o); err == nil || !strings.Contains(err.Error(), "sandboxer image build") {
			t.Errorf("missing image = %v, want the build-image hint", err)
		}
	})
}

func TestExecSession(t *testing.T) {
	requireExec(t, "sh")
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	engine := filepath.Join(dir, "engine")
	writeEngineScript(t, engine, logPath)
	o := RunOpts{
		MountDest: true, Engine: engine, Dest: "/d",
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
	}
	t.Setenv("TERM", "")

	code, err := ExecSession(o, "n", []string{"echo", "hi"})
	if err != nil || code != 0 {
		t.Fatalf("ExecSession = (%d, %v), want (0, nil)", code, err)
	}
	if lines := engineLog(t, logPath); !hasLine(lines, "exec -i -w /d n echo hi") {
		t.Errorf("exec argv wrong:\n%s", strings.Join(lines, "\n"))
	}

	// The in-container command's exit code propagates, not an error.
	t.Setenv("SBX_EXIT", "7")
	if code, err = ExecSession(o, "n", []string{"false"}); err != nil || code != 7 {
		t.Errorf("ExecSession = (%d, %v), want (7, nil)", code, err)
	}
}

// TestSessionIdle pins the probe that decides whether a stale-but-running
// session may be rebuilt: it must answer "idle" ONLY on a positive finding
// (an empty listing, or tmux reporting no server). Every other outcome —
// engine error, no tmux in the image — has to read as busy, because a wrong
// "idle" destroys a running agent while a wrong "busy" only postpones a
// config change.
func TestSessionIdle(t *testing.T) {
	requireExec(t, "sh")
	engine, logPath := sessionEngine(t)

	t.Setenv("SBX_EXEC_OUT", "")
	if !SessionIdle(engine, "n") {
		t.Error("an empty listing must read as idle")
	}
	if lines := engineLog(t, logPath); !hasLine(lines, "exec n tmux -L sandboxer list-sessions") {
		t.Errorf("probe argv wrong:\n%s", strings.Join(lines, "\n"))
	}

	t.Setenv("SBX_EXEC_OUT", "main: 1 windows (created ...) (attached)")
	if SessionIdle(engine, "n") {
		t.Error("a listed tmux session must read as busy")
	}

	// tmux's own "nothing is running" answer is a non-zero exit, not a failure.
	t.Setenv("SBX_FAIL_ON", "exec")
	t.Setenv("SBX_STDERR", "no server running on /tmp/tmux-1000/sandboxer")
	if !SessionIdle(engine, "n") {
		t.Error("tmux reporting no server must read as idle")
	}

	// Anything else that fails is NOT evidence of emptiness.
	t.Setenv("SBX_STDERR", `exec: "tmux": executable file not found in $PATH`)
	if SessionIdle(engine, "n") {
		t.Error("an unexplained engine failure must read as busy, never idle")
	}
}

func TestStopSession(t *testing.T) {
	requireExec(t, "sh")
	name := SessionName("s", "/b")

	t.Run("stops container and running proxy", func(t *testing.T) {
		engine, logPath := sessionEngine(t)
		t.Setenv("SBX_INSPECT_OUT", "true h")
		t.Setenv("SBX_PROXY_RUNNING", "1")
		if err := StopSession(engine, "s", "/b"); err != nil {
			t.Fatalf("StopSession: %v", err)
		}
		lines := engineLog(t, logPath)
		for _, want := range []string{"stop " + name, "stop " + name + "-proxy"} {
			if !hasLine(lines, want) {
				t.Errorf("missing %q:\n%s", want, strings.Join(lines, "\n"))
			}
		}
		// Networks stay: stop is resumable, only rm tears them down.
		if findPrefixLine(lines, "network rm") != "" {
			t.Errorf("stop must keep the networks:\n%s", strings.Join(lines, "\n"))
		}
	})

	t.Run("missing session is a no-op", func(t *testing.T) {
		engine, logPath := sessionEngine(t)
		t.Setenv("SBX_INSPECT_FAIL", "1")
		if err := StopSession(engine, "s", "/b"); err != nil {
			t.Fatalf("StopSession on nothing: %v", err)
		}
		if findPrefixLine(engineLog(t, logPath), "stop ") != "" {
			t.Error("nothing existed, nothing should be stopped")
		}
	})

	t.Run("stop failure surfaces", func(t *testing.T) {
		engine, _ := sessionEngine(t)
		t.Setenv("SBX_INSPECT_OUT", "true h")
		t.Setenv("SBX_FAIL_ON", "stop")
		if err := StopSession(engine, "s", "/b"); err == nil || !strings.Contains(err.Error(), "stop session") {
			t.Errorf("StopSession = %v, want a stop session error", err)
		}
	})

	t.Run("proxy stop failure surfaces", func(t *testing.T) {
		engine, _ := sessionEngine(t)
		t.Setenv("SBX_INSPECT_FAIL", "1") // no container, but a running proxy
		t.Setenv("SBX_PROXY_RUNNING", "1")
		t.Setenv("SBX_FAIL_ON", "stop")
		if err := StopSession(engine, "s", "/b"); err == nil {
			t.Error("a failing proxy stop should surface")
		}
	})
}

func TestRemoveSession(t *testing.T) {
	requireExec(t, "sh")
	name := SessionName("s", "/b")

	t.Run("removes container and egress", func(t *testing.T) {
		engine, logPath := sessionEngine(t)
		t.Setenv("SBX_INSPECT_OUT", "true h")
		if err := RemoveSession(engine, "s", "/b"); err != nil {
			t.Fatalf("RemoveSession: %v", err)
		}
		lines := engineLog(t, logPath)
		for _, want := range []string{
			"rm -f " + name,
			"rm -f " + name + "-proxy",
			"network rm " + name + "-int",
			"network rm " + name + "-ext",
		} {
			if !hasLine(lines, want) {
				t.Errorf("missing %q:\n%s", want, strings.Join(lines, "\n"))
			}
		}
	})

	t.Run("missing session still sweeps egress", func(t *testing.T) {
		engine, logPath := sessionEngine(t)
		t.Setenv("SBX_INSPECT_FAIL", "1")
		if err := RemoveSession(engine, "s", "/b"); err != nil {
			t.Fatalf("RemoveSession on nothing: %v", err)
		}
		lines := engineLog(t, logPath)
		if hasLine(lines, "rm -f "+name) {
			t.Error("no container existed, none should be removed")
		}
		if !hasLine(lines, "rm -f "+name+"-proxy") {
			t.Errorf("egress sweep skipped:\n%s", strings.Join(lines, "\n"))
		}
	})

	t.Run("rm failure surfaces", func(t *testing.T) {
		engine, _ := sessionEngine(t)
		t.Setenv("SBX_INSPECT_OUT", "true h")
		t.Setenv("SBX_FAIL_ON", "rm")
		t.Setenv("SBX_STDERR", "Error: container state improper")
		err := RemoveSession(engine, "s", "/b")
		if err == nil || !strings.Contains(err.Error(), "remove session") {
			t.Errorf("RemoveSession = %v, want a remove session error", err)
		}
		if err != nil && !strings.Contains(err.Error(), "container state improper") {
			t.Errorf("RemoveSession dropped the engine's stderr: %v", err)
		}
	})
}

// namedEngines writes stub binaries under the given names into one directory
// and makes it the ONLY PATH entry, so InstalledEngines/SweepEngines discover
// exactly these (and no smolvm/msb). present names the engines whose `container
// inspect` reports a live session; every other invocation just logs and
// succeeds. It returns each engine's call log path by name.
func namedEngines(t *testing.T, names []string, present map[string]bool) map[string]string {
	t.Helper()
	dir := t.TempDir()
	logs := map[string]string{}
	for _, n := range names {
		logs[n] = filepath.Join(dir, n+".log")
		body := `[ "$1" = container ] && exit 1
`
		if present[n] {
			body = `[ "$1" = container ] && { echo "true h img"; exit 0; }
`
		}
		script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + logs[n] + "\n" + body + "exit 0\n"
		if err := os.WriteFile(filepath.Join(dir, n), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	return logs
}

// TestRemoveSessionAnywhere: a teardown asks EVERY engine on the host, because
// the profile's backend only says where the next session would be created — a
// session created before a `backend =` edit lives wherever it lives, and asking
// only the currently-resolved engine left it running with its state dir gone
// (nothing could ever reclaim it).
func TestRemoveSessionAnywhere(t *testing.T) {
	requireExec(t, "sh")
	name := SessionName("s", "/b")

	t.Run("removes the session from the engine that actually holds it", func(t *testing.T) {
		logs := namedEngines(t, []string{"podman", "docker"}, map[string]bool{"podman": true})
		removed, err := RemoveSessionAnywhere("s", "/b", config.Defaults{})
		if err != nil {
			t.Fatalf("RemoveSessionAnywhere: %v", err)
		}
		if !slices.Equal(removed, []string{"podman"}) {
			t.Errorf("removed = %v, want [podman]", removed)
		}
		if !hasLine(engineLog(t, logs["podman"]), "rm -f "+name) {
			t.Errorf("the holding engine was not swept:\n%s", strings.Join(engineLog(t, logs["podman"]), "\n"))
		}
		// The other engine is still asked (and its egress leftovers swept), it
		// just has no container to remove.
		if hasLine(engineLog(t, logs["docker"]), "rm -f "+name) {
			t.Error("docker held no session, nothing should have been removed there")
		}
		if !hasLine(engineLog(t, logs["docker"]), "rm -f "+name+"-proxy") {
			t.Error("docker was not asked at all")
		}
	})

	t.Run("nothing anywhere reports nothing removed", func(t *testing.T) {
		namedEngines(t, []string{"podman", "docker"}, nil)
		removed, err := RemoveSessionAnywhere("s", "/b", config.Defaults{})
		if err != nil || len(removed) != 0 {
			t.Errorf("RemoveSessionAnywhere on nothing = (%v, %v), want ([], nil)", removed, err)
		}
	})

	t.Run("one failing engine does not strand the others", func(t *testing.T) {
		dir := t.TempDir()
		// Both hold the session; podman refuses to remove it, docker must still
		// be swept and the failure must surface rather than read as "all clean".
		podman := "#!/bin/sh\n[ \"$1\" = container ] && { echo \"true h img\"; exit 0; }\n" +
			"[ \"$1\" = rm ] && exit 1\nexit 0\n"
		if err := os.WriteFile(filepath.Join(dir, "podman"), []byte(podman), 0o755); err != nil {
			t.Fatal(err)
		}
		log := filepath.Join(dir, "docker.log")
		docker := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + log + "\n" +
			"[ \"$1\" = container ] && { echo \"true h img\"; exit 0; }\nexit 0\n"
		if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(docker), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir)
		removed, err := RemoveSessionAnywhere("s", "/b", config.Defaults{})
		if err == nil || !strings.Contains(err.Error(), "remove session") {
			t.Errorf("RemoveSessionAnywhere = %v, want the failing engine to surface", err)
		}
		if !slices.Equal(removed, []string{"docker"}) {
			t.Errorf("removed = %v, want [docker] — one bad engine cannot strand the rest", removed)
		}
		if !hasLine(engineLog(t, log), "rm -f "+name) {
			t.Error("the second engine was not swept after the first failed")
		}
	})
}

// TestSessionEngine: the operations that act on the ONE live session (stop, the
// tmux capture) must find it where it is, not where the profile points.
func TestSessionEngine(t *testing.T) {
	requireExec(t, "sh")

	t.Run("names the engine holding the session", func(t *testing.T) {
		namedEngines(t, []string{"podman", "docker"}, map[string]bool{"docker": true})
		if got := SessionEngine("s", "/b", config.Defaults{}); got != "docker" {
			t.Errorf("SessionEngine = %q, want docker", got)
		}
	})

	t.Run("no session anywhere is empty", func(t *testing.T) {
		namedEngines(t, []string{"podman", "docker"}, nil)
		if got := SessionEngine("s", "/b", config.Defaults{}); got != "" {
			t.Errorf("SessionEngine = %q, want \"\"", got)
		}
	})
}

func TestRemoveAllSessions(t *testing.T) {
	requireExec(t, "sh")

	t.Run("removes every labeled session", func(t *testing.T) {
		engine, logPath := sessionEngine(t)
		t.Setenv("SBX_PS_OUT", "n1\nn2")
		t.Setenv("SBX_INSPECT_OUT", "true h")
		if err := RemoveAllSessions(engine, "/b"); err != nil {
			t.Fatalf("RemoveAllSessions: %v", err)
		}
		lines := engineLog(t, logPath)
		wantPS := "ps -a --filter label=sandboxer.managed=true --filter label=sandboxer.base=/b --format {{.Names}}"
		for _, want := range []string{wantPS, "rm -f n1", "rm -f n2", "rm -f n1-proxy", "network rm n2-int"} {
			if !hasLine(lines, want) {
				t.Errorf("missing %q:\n%s", want, strings.Join(lines, "\n"))
			}
		}
	})

	t.Run("no sessions is a no-op", func(t *testing.T) {
		engine, logPath := sessionEngine(t)
		t.Setenv("SBX_PS_OUT", "")
		if err := RemoveAllSessions(engine, "/b"); err != nil {
			t.Fatalf("RemoveAllSessions on nothing: %v", err)
		}
		if findPrefixLine(engineLog(t, logPath), "rm ") != "" {
			t.Error("nothing listed, nothing should be removed")
		}
	})

	t.Run("ps failure surfaces", func(t *testing.T) {
		engine, _ := sessionEngine(t)
		t.Setenv("SBX_PS_FAIL", "1")
		if err := RemoveAllSessions(engine, "/b"); err == nil || !strings.Contains(err.Error(), "list sessions") {
			t.Errorf("RemoveAllSessions = %v, want a list sessions error", err)
		}
	})
}

func TestOrphanSessions(t *testing.T) {
	requireExec(t, "sh")

	t.Run("flags only the gone-base containers", func(t *testing.T) {
		engine, logPath := sessionEngine(t)
		alive := t.TempDir()
		gone := filepath.Join(t.TempDir(), "deleted-project", ".sandboxer")
		// n2 has no base label (empty line) — its slot must not shift n3's base.
		t.Setenv("SBX_PS_OUT", "n1\nn2\nn3")
		t.Setenv("SBX_INSPECT_OUT", alive+"\n\n"+gone)
		got, err := OrphanSessions(engine)
		if err != nil {
			t.Fatalf("OrphanSessions: %v", err)
		}
		if !slices.Equal(got, []string{"n3"}) {
			t.Errorf("OrphanSessions = %v, want [n3]", got)
		}
		lines := engineLog(t, logPath)
		for _, want := range []string{
			"ps -a --filter label=sandboxer.managed=true --format {{.Names}}",
			`container inspect --format {{index .Config.Labels "sandboxer.base"}} n1 n2 n3`,
		} {
			if !hasLine(lines, want) {
				t.Errorf("missing %q:\n%s", want, strings.Join(lines, "\n"))
			}
		}
	})

	t.Run("no sessions skips the inspect", func(t *testing.T) {
		engine, logPath := sessionEngine(t)
		t.Setenv("SBX_PS_OUT", "")
		got, err := OrphanSessions(engine)
		if err != nil || got != nil {
			t.Fatalf("OrphanSessions = (%v, %v), want (nil, nil)", got, err)
		}
		if findPrefixLine(engineLog(t, logPath), "container inspect") != "" {
			t.Error("no names, no inspect")
		}
	})

	t.Run("short inspect output never flags a false orphan", func(t *testing.T) {
		engine, _ := sessionEngine(t)
		gone := filepath.Join(t.TempDir(), "deleted-project", ".sandboxer")
		// Two names but only one inspect line: n2 has no base slot, so the
		// scan stops there instead of guessing (or panicking).
		t.Setenv("SBX_PS_OUT", "n1\nn2")
		t.Setenv("SBX_INSPECT_OUT", gone)
		got, err := OrphanSessions(engine)
		if err != nil {
			t.Fatalf("OrphanSessions: %v", err)
		}
		if !slices.Equal(got, []string{"n1"}) {
			t.Errorf("OrphanSessions = %v, want [n1]", got)
		}
	})

	t.Run("engine failures surface", func(t *testing.T) {
		engine, _ := sessionEngine(t)
		t.Setenv("SBX_PS_FAIL", "1")
		if _, err := OrphanSessions(engine); err == nil || !strings.Contains(err.Error(), "list sessions") {
			t.Errorf("OrphanSessions = %v, want a list sessions error", err)
		}
		t.Setenv("SBX_PS_FAIL", "")
		t.Setenv("SBX_PS_OUT", "n1")
		t.Setenv("SBX_INSPECT_FAIL", "1")
		if _, err := OrphanSessions(engine); err == nil || !strings.Contains(err.Error(), "inspect sessions") {
			t.Errorf("OrphanSessions = %v, want an inspect sessions error", err)
		}
	})
}

func TestSessionStates(t *testing.T) {
	requireExec(t, "sh")

	t.Run("maps slug to status", func(t *testing.T) {
		engine, logPath := sessionEngine(t)
		t.Setenv("SBX_PS_OUT", "n1\nn2")
		// Status comes first because a raw slug may contain spaces. A line
		// without a separator ("malformed") and one with an empty slug
		// ("created ") are skipped, never mapped.
		t.Setenv("SBX_INSPECT_OUT", "running slug one\nmalformed\ncreated \nexited s2")
		got, err := SessionStates(engine, "/b")
		if err != nil {
			t.Fatalf("SessionStates: %v", err)
		}
		want := map[string]string{"slug one": "running", "s2": "exited"}
		if !maps.Equal(got, want) {
			t.Errorf("SessionStates = %v, want %v", got, want)
		}
		// One batched inspect over all the names ps returned.
		wantInspect := `container inspect --format {{.State.Status}} {{index .Config.Labels "sandboxer.slug"}} n1 n2`
		if lines := engineLog(t, logPath); !hasLine(lines, wantInspect) {
			t.Errorf("missing %q:\n%s", wantInspect, strings.Join(lines, "\n"))
		}
	})

	t.Run("no sessions skips the inspect", func(t *testing.T) {
		engine, logPath := sessionEngine(t)
		t.Setenv("SBX_PS_OUT", "")
		got, err := SessionStates(engine, "/b")
		if err != nil || len(got) != 0 {
			t.Fatalf("SessionStates = (%v, %v), want an empty map", got, err)
		}
		if findPrefixLine(engineLog(t, logPath), "container inspect") != "" {
			t.Error("no names, no inspect")
		}
	})

	t.Run("engine failures surface", func(t *testing.T) {
		engine, _ := sessionEngine(t)
		t.Setenv("SBX_PS_FAIL", "1")
		if _, err := SessionStates(engine, "/b"); err == nil {
			t.Error("ps failure should surface")
		}
		t.Setenv("SBX_PS_FAIL", "")
		t.Setenv("SBX_PS_OUT", "n1")
		t.Setenv("SBX_INSPECT_FAIL", "1")
		if _, err := SessionStates(engine, "/b"); err == nil || !strings.Contains(err.Error(), "inspect sessions") {
			t.Errorf("SessionStates = %v, want an inspect sessions error", err)
		}
	})
}

func TestAllSessionStates(t *testing.T) {
	requireExec(t, "sh")

	t.Run("groups every session by its base dir", func(t *testing.T) {
		engine, logPath := sessionEngine(t)
		t.Setenv("SBX_PS_OUT", "n1\nn2\nn3\nn4\nn5")
		// The base dir comes LAST and takes the rest of the line: a status and a
		// slug never contain a space, a project path can. A session missing
		// either label belongs to no project and is skipped, never mapped.
		t.Setenv("SBX_INSPECT_OUT", strings.Join([]string{
			"running feat /state/a",
			"exited other /state/a",
			"running spike /state/with space/b",
			"created  /state/a",
			"running noBase ",
		}, "\n"))
		got, err := AllSessionStates(engine)
		if err != nil {
			t.Fatalf("AllSessionStates: %v", err)
		}
		want := map[string]map[string]string{
			"/state/a":            {"feat": "running", "other": "exited"},
			"/state/with space/b": {"spike": "running"},
		}
		if len(got) != len(want) {
			t.Fatalf("AllSessionStates = %v, want %v", got, want)
		}
		for base, states := range want {
			if !maps.Equal(got[base], states) {
				t.Errorf("AllSessionStates[%q] = %v, want %v", base, got[base], states)
			}
		}
		// One ps and one batched inspect, whatever the number of projects.
		for _, want := range []string{
			"ps -a --filter label=sandboxer.managed=true --format {{.Names}}",
			`container inspect --format {{.State.Status}} {{index .Config.Labels "sandboxer.slug"}} {{index .Config.Labels "sandboxer.base"}} n1 n2 n3 n4 n5`,
		} {
			if lines := engineLog(t, logPath); !hasLine(lines, want) {
				t.Errorf("missing %q:\n%s", want, strings.Join(lines, "\n"))
			}
		}
	})

	t.Run("no sessions skips the inspect", func(t *testing.T) {
		engine, logPath := sessionEngine(t)
		t.Setenv("SBX_PS_OUT", "")
		got, err := AllSessionStates(engine)
		if err != nil || len(got) != 0 {
			t.Fatalf("AllSessionStates = (%v, %v), want an empty map", got, err)
		}
		if findPrefixLine(engineLog(t, logPath), "container inspect") != "" {
			t.Error("no names, no inspect")
		}
	})

	t.Run("engine failures surface", func(t *testing.T) {
		engine, _ := sessionEngine(t)
		t.Setenv("SBX_PS_FAIL", "1")
		if _, err := AllSessionStates(engine); err == nil || !strings.Contains(err.Error(), "list sessions") {
			t.Errorf("AllSessionStates = %v, want a list sessions error", err)
		}
		t.Setenv("SBX_PS_FAIL", "")
		t.Setenv("SBX_PS_OUT", "n1")
		t.Setenv("SBX_INSPECT_FAIL", "1")
		if _, err := AllSessionStates(engine); err == nil || !strings.Contains(err.Error(), "inspect sessions") {
			t.Errorf("AllSessionStates = %v, want an inspect sessions error", err)
		}
	})
}
