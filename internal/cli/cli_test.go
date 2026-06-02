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
	if file, pos := resolveProfileFile("cfg.yaml", "leftover"); file != "cfg.yaml" || pos != "leftover" {
		t.Errorf("config precedence = (%q,%q)", file, pos)
	}
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("p.yaml", []byte("name: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if file, pos := resolveProfileFile("", "p.yaml"); file != "p.yaml" || pos != "" {
		t.Errorf("positional yaml = (%q,%q)", file, pos)
	}
	if err := os.WriteFile("sandboxer.yaml", []byte("name: y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if file, pos := resolveProfileFile("", ""); file != "sandboxer.yaml" || pos != "" {
		t.Errorf("auto-discovery = (%q,%q)", file, pos)
	}
	if file, pos := resolveProfileFile("", "slug"); file != "" || pos != "slug" {
		t.Errorf("non-yaml positional = (%q,%q)", file, pos)
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
// ensures the in-container guard is off.
func newProject(t *testing.T) string {
	t.Helper()
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
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

	// No profile → an empty sandbox (nothing is copied by default).
	if code, out, errs := run("create", "feat", "--src", project); code != 0 || !strings.Contains(out, "created") {
		t.Fatalf("create = (%d, %q, %q)", code, out, errs)
	}
	dest := filepath.Join(project, ".sandboxer", "feat")
	if fi, err := os.Stat(dest); err != nil || !fi.IsDir() {
		t.Errorf("sandbox dir not created: %v", err)
	}
	if entries, _ := os.ReadDir(dest); len(entries) != 0 {
		t.Errorf("sandbox should be empty without srcs, has %d entries", len(entries))
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
	if code, out, _ := run("show", "feat", "--src", project); code != 0 || !strings.Contains(out, "no profile") {
		t.Errorf("show = (%d, %q)", code, out)
	}
	if code, out, _ := run("rm", "feat", "--src", project); code != 0 || !strings.Contains(out, "removed") {
		t.Errorf("rm = (%d, %q)", code, out)
	}
	if code, out, _ := run("rm-all", project); code != 0 || !strings.Contains(out, "removed") {
		t.Errorf("rm-all = (%d, %q)", code, out)
	}
	if _, err := os.Stat(filepath.Join(project, ".sandboxer")); !os.IsNotExist(err) {
		t.Error(".sandboxer should be gone after rm-all")
	}
}

func TestRunInContainerRestriction(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "1")
	code, _, errs := run("create", "x", "--src", t.TempDir())
	if code != 1 {
		t.Errorf("create in container exit = %d, want 1", code)
	}
	if !strings.Contains(errs, "not available inside the container") {
		t.Errorf("missing restriction message: %q", errs)
	}
}

func TestBaseOnlyNoState(t *testing.T) {
	if _, err := baseOnly(t.TempDir()); err == nil {
		t.Error("baseOnly should error without existing state")
	}
}
