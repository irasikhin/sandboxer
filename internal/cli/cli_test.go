package cli

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/config"
)

// --- pure helpers -----------------------------------------------------------

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "", "b"); got != "b" {
		t.Errorf("firstNonEmpty = %q, want b", got)
	}
	if got := firstNonEmpty("a", "b"); got != "a" {
		t.Errorf("firstNonEmpty = %q, want a", got)
	}
	if got := firstNonEmpty(); got != "" {
		t.Errorf("firstNonEmpty() = %q, want empty", got)
	}
}

func TestPosArg(t *testing.T) {
	if posArg([]string{"a", "b"}) != "a" {
		t.Error("posArg should return the first arg")
	}
	if posArg(nil) != "" {
		t.Error("posArg(nil) should be empty")
	}
}

func TestIsYAML(t *testing.T) {
	for in, want := range map[string]bool{"a.yaml": true, "a.yml": true, "a.json": false, "a": false} {
		if isYAML(in) != want {
			t.Errorf("isYAML(%q) = %v, want %v", in, !want, want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 3); got != "he…" {
		t.Errorf("truncate should cut to n with an ellipsis, got %q", got)
	}
	if truncate("hi", 5) != "hi" {
		t.Error("truncate should leave short strings")
	}
	// Truncation is rune-aware: it must not split a multi-byte rune.
	if got := truncate("héllo", 3); got != "hé…" {
		t.Errorf("truncate should be rune-safe, got %q", got)
	}
}

func TestFileExistsAndInContainer(t *testing.T) {
	f := filepath.Join(t.TempDir(), "f")
	if fileExists(f) {
		t.Error("missing file reported existing")
	}
	if err := os.WriteFile(f, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if !fileExists(f) {
		t.Error("existing file reported missing")
	}
	t.Setenv("SANDBOXER_IN_CONTAINER", "1")
	if !inContainer() {
		t.Error("inContainer should be true when env set")
	}
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	if inContainer() {
		t.Error("inContainer should be false when env empty")
	}
}

func TestReadMeta(t *testing.T) {
	p := filepath.Join(t.TempDir(), "m")
	if err := os.WriteFile(p, []byte("exit=0\nsecs=12\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if e, s := readMeta(p); e != "0" || s != "12" {
		t.Errorf("readMeta = (%q,%q), want (0,12)", e, s)
	}
	if e, s := readMeta(filepath.Join(t.TempDir(), "missing")); e != "-" || s != "-" {
		t.Errorf("readMeta(missing) = (%q,%q), want (-,-)", e, s)
	}
}

func TestJSONResult(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if got := jsonResult(write("r.json", `{"result":"hello   world"}`)); got != "hello world" {
		t.Errorf("result = %q, want 'hello world'", got)
	}
	if got := jsonResult(write("e.json", `{"error":"boom"}`)); got != "boom" {
		t.Errorf("error = %q, want boom", got)
	}
	if got := jsonResult(write("bad.json", `not json`)); got != "" {
		t.Errorf("invalid json = %q, want empty", got)
	}
	if got := jsonResult(filepath.Join(dir, "missing")); got != "" {
		t.Errorf("missing = %q, want empty", got)
	}
}

func TestDumpFile(t *testing.T) {
	dir := t.TempDir()
	noNL := filepath.Join(dir, "a")
	_ = os.WriteFile(noNL, []byte("abc"), 0o644)
	var buf bytes.Buffer
	if !dumpFile(&buf, noNL) || buf.String() != "abc\n" {
		t.Errorf("dumpFile appends newline: got %q", buf.String())
	}
	buf.Reset()
	withNL := filepath.Join(dir, "b")
	_ = os.WriteFile(withNL, []byte("abc\n"), 0o644)
	if !dumpFile(&buf, withNL) || buf.String() != "abc\n" {
		t.Errorf("dumpFile keeps single newline: got %q", buf.String())
	}
	if dumpFile(&buf, filepath.Join(dir, "missing")) {
		t.Error("dumpFile(missing) should report false")
	}
}

func TestSplitDash(t *testing.T) {
	run := func(args []string) (string, []string) {
		var pos string
		var rest []string
		c := &cobra.Command{Use: "x", RunE: func(cmd *cobra.Command, a []string) error {
			pos, rest = splitDash(cmd, a)
			return nil
		}}
		c.SetArgs(args)
		c.SetOut(io.Discard)
		c.SetErr(io.Discard)
		if err := c.Execute(); err != nil {
			t.Fatalf("execute %v: %v", args, err)
		}
		return pos, rest
	}
	if pos, rest := run([]string{"slug", "--", "echo", "hi"}); pos != "slug" || strings.Join(rest, " ") != "echo hi" {
		t.Errorf("with dash = (%q,%v)", pos, rest)
	}
	if pos, rest := run([]string{"slug"}); pos != "slug" || rest != nil {
		t.Errorf("no dash = (%q,%v)", pos, rest)
	}
	if pos, rest := run([]string{"--", "cmd"}); pos != "" || strings.Join(rest, " ") != "cmd" {
		t.Errorf("dash only = (%q,%v)", pos, rest)
	}
}

func TestResolveProfileFile(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	// Hermetic store: an empty dir until a case populates it.
	store := t.TempDir()
	t.Setenv("SANDBOXER_PROFILES", store)

	must := func(label, wantFile, wantPos, gotFile, gotPos string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", label, err)
		}
		if gotFile != wantFile || gotPos != wantPos {
			t.Errorf("%s = (%q,%q), want (%q,%q)", label, gotFile, gotPos, wantFile, wantPos)
		}
	}

	// -f *.yaml wins; the positional is kept as a slug override.
	f, p, err := resolveProfileFile("cfg.yaml", ".", "leftover")
	must("config precedence", "cfg.yaml", "leftover", f, p, err)

	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("p.yaml", []byte("name: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, p, err = resolveProfileFile("", ".", "p.yaml")
	must("positional yaml", "p.yaml", "", f, p, err)

	if err := os.WriteFile(config.ConfigPath(), []byte("name: y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, p, err = resolveProfileFile("", ".", "")
	must("auto-discovery", config.ConfigPath(), "", f, p, err)

	// A bare positional naming nothing stays a slug.
	f, p, err = resolveProfileFile("", ".", "slug")
	must("non-yaml positional", "", "slug", f, p, err)

	// A named profile from the global store, by positional and by -f NAME.
	web := filepath.Join(store, "web.yaml")
	if err := os.WriteFile(web, []byte("name: web\nbackend: docker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, p, err = resolveProfileFile("", ".", "web")
	must("store by positional", web, "", f, p, err)
	f, p, err = resolveProfileFile("web", ".", "")
	must("store by -f name", web, "", f, p, err)

	// -f DIR selects by name; an unknown name errors with the listing.
	envs := t.TempDir()
	api := filepath.Join(envs, "api.yaml")
	if err := os.WriteFile(api, []byte("name: api\nbackend: podman\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, p, err = resolveProfileFile(envs, ".", "api")
	must("dir by name", api, "", f, p, err)
	if _, _, err := resolveProfileFile(envs, ".", "nope"); err == nil {
		t.Error("unknown profile in -f dir should error")
	}
}

// TestResolveProfileFileUsesRoot pins the fix: an existing project config is
// discovered under the given root (--src), not only the process cwd.
func TestResolveProfileFileUsesRoot(t *testing.T) {
	t.Chdir(t.TempDir()) // cwd deliberately has no config

	root := t.TempDir()
	cfg := config.ConfigPathIn(root)

	// Multi-profile config under root: a positional selects a section, found via root.
	if err := os.WriteFile(cfg, []byte("profiles:\n  svc:\n    backend: docker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if f, p, err := resolveProfileFile("", root, "svc"); err != nil || f != cfg || p != "svc" {
		t.Errorf("multi under root = (%q,%q,%v), want (%q,\"svc\",nil)", f, p, err, cfg)
	}
	// The same positional from a config-less cwd finds nothing (stays a bare slug).
	if f, _, _ := resolveProfileFile("", ".", "svc"); f != "" {
		t.Errorf("cwd discovery = %q, want empty (no project config in cwd)", f)
	}

	// Flat config under root: no positional, discovered via root.
	if err := os.WriteFile(cfg, []byte("name: svc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if f, _, err := resolveProfileFile("", root, ""); err != nil || f != cfg {
		t.Errorf("flat under root = (%q,%v), want (%q,nil)", f, err, cfg)
	}
}

// --- end-to-end through Run() ----------------------------------------------

func run(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := Run(args, strings.NewReader(""), &out, &errb)
	return code, out.String(), errb.String()
}

func requireExec(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		if _, err := exec.LookPath(n); err != nil {
			t.Skipf("%s not available", n)
		}
	}
}

// newProject returns a fresh project dir (plain, no git) with one file, and
// ensures the in-container guard is off. Auto-scaffold is left enabled — a
// bare create/enter without a profile writes a default sandboxer.yaml so
// the user never lands in an empty no-profile state.
func newProject(t *testing.T) string {
	t.Helper()
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	// Isolate the global profile store so a bare slug never resolves to a
	// host-installed named profile.
	t.Setenv("SANDBOXER_PROFILES", t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// sandboxer is git-only: a sandbox is a worktree of the project repo, so the
	// project must be a git repo with at least one commit.
	gitInitProject(t, dir)
	return dir
}

// gitInitProject makes dir a git repo with a single commit so ResolveBase detects
// it and MakeSandbox can branch a worktree off HEAD. It skips the test (not
// fatal) when git is unavailable — sandboxer needs git, but the unit tests that
// don't touch a sandbox shouldn't hard-fail on a git-less host.
func gitInitProject(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "t"},
		{"config", "commit.gpgsign", "false"},
		{"add", "-A"},
		{"commit", "--allow-empty", "-qm", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Skipf("git %v unusable: %v (%s)", args, err, out)
		}
	}
}

func TestRunAgentsVersionHelp(t *testing.T) {
	if code, out, _ := run("agents"); code != 0 || !strings.Contains(out, "claude") {
		t.Errorf("agents = (%d, %q)", code, out)
	}
	if code, out, _ := run("--version"); code != 0 || !strings.Contains(out, Version) {
		t.Errorf("--version = (%d, %q)", code, out)
	}
	if code, out, _ := run("--help"); code != 0 || !strings.Contains(out, "sandboxer") {
		t.Errorf("--help = (%d, %q)", code, out)
	}
	if code, _, _ := run("totally-bogus-command"); code != 1 {
		t.Errorf("unknown command exit = %d, want 1", code)
	}
}

func TestRunLifecycle(t *testing.T) {
	project := newProject(t)

	// Auto-scaffold fires on first create; an empty sandbox is created.
	if code, out, errs := run("create", "feat", "--src", project); code != 0 || !strings.Contains(out, "created") {
		t.Fatalf("create = (%d, %q, %q)", code, out, errs)
	}
	dest := stateDir(project, "feat")
	if fi, err := os.Stat(dest); err != nil || !fi.IsDir() {
		t.Errorf("sandbox dir not created: %v", err)
	}
	if code, out, _ := run("list", "--src", project); code != 0 || !strings.Contains(out, "feat") {
		t.Errorf("list = (%d, %q)", code, out)
	}
	if code, _, _ := run("use", "feat", "--src", project); code != 0 {
		t.Errorf("use set exit code")
	}
	if code, out, _ := run("use", "--src", project); code != 0 || !strings.Contains(out, "feat") {
		t.Errorf("use get = (%d, %q)", code, out)
	}
	if code, out, _ := run("show", "feat", "--src", project); code != 0 || !strings.Contains(out, "profile") {
		t.Errorf("show = (%d, %q)", code, out)
	}
	if code, out, _ := run("rm", "feat", "--src", project); code != 0 || !strings.Contains(out, "removed") {
		t.Errorf("rm = (%d, %q)", code, out)
	}
	if code, out, _ := run("clean", "--force", project); code != 0 || !strings.Contains(out, "removed") {
		t.Errorf("clean = (%d, %q)", code, out)
	}
	if _, err := os.Stat(stateDir(project)); !os.IsNotExist(err) {
		t.Error("state dir should be gone after clean")
	}
	// clean without --force is rejected.
	if code, _, errs := run("clean", project); code != 1 || !strings.Contains(errs, "--force") {
		t.Errorf("clean without --force = (%d, %q); want error mentioning --force", code, errs)
	}
}

func TestRunInContainerRestriction(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "1")
	code, _, errs := run("create", "x", "--src", t.TempDir())
	if code != 1 {
		t.Errorf("create in container exit = %d, want 1", code)
	}
	if !strings.Contains(errs, "not available inside the sandbox") {
		t.Errorf("missing restriction message: %q", errs)
	}
}

func TestBaseOnlyNoState(t *testing.T) {
	if _, err := baseOnly(t.TempDir()); err == nil {
		t.Error("baseOnly should error without existing state")
	}
}
