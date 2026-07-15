package sandbox

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
)

// TestMain isolates runtime state into a throwaway directory so the suite never
// writes into the developer's real ~/.local/state. Each test still uses a unique
// project root (t.TempDir()), which config.StateDir maps to a unique subdir.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sandboxer-state-test-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("SANDBOXER_STATE", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveBaseSeedsState(t *testing.T) {
	src := t.TempDir()
	if RunEnvExists(src) {
		t.Error("RunEnvExists should be false before ResolveBase")
	}
	b, err := ResolveBase(src)
	if err != nil {
		t.Fatalf("ResolveBase: %v", err)
	}
	if !RunEnvExists(src) {
		t.Error("RunEnvExists should be true after ResolveBase")
	}
	if _, err := os.Stat(filepath.Join(b.metaDir(), "run.env")); err != nil {
		t.Errorf("run.env not seeded: %v", err)
	}
	if _, err := os.Stat(b.AgentsListPath()); err != nil {
		t.Errorf("agents.list not seeded: %v", err)
	}
	if b.Domains != config.DefaultDomains {
		t.Errorf("Domains = %q, want defaults", b.Domains)
	}
	data, _ := os.ReadFile(filepath.Join(b.metaDir(), "run.env"))
	if strings.Contains(string(data), "IS_GIT") || strings.Contains(string(data), "BASE_SHA") {
		t.Errorf("run.env should not contain git fields:\n%s", data)
	}
	if _, err := ResolveBase(src); err != nil {
		t.Fatalf("re-ResolveBase: %v", err)
	}
}

// TestResolveBaseStateOutsideProject pins the config/data split: runtime state
// lives under config.StateDir (outside the project root), NOT under the project's
// the repo, so it can never be committed by accident. No .gitignore is written
// into the project anymore.
func TestResolveBaseStateOutsideProject(t *testing.T) {
	src := t.TempDir()
	b, err := ResolveBase(src)
	if err != nil {
		t.Fatalf("ResolveBase: %v", err)
	}
	if b.Dir != config.StateDir(src) {
		t.Errorf("Base.Dir = %q, want config.StateDir = %q", b.Dir, config.StateDir(src))
	}
	if strings.HasPrefix(b.Dir, src) {
		t.Errorf("state dir %q must live OUTSIDE the project root %q", b.Dir, src)
	}
	// Nothing is written into the project — no gitignore guard.
	if _, err := os.Stat(filepath.Join(src, config.LegacyStateDirName)); !os.IsNotExist(err) {
		t.Errorf("ResolveBase must not create %s/ in the project (err=%v)", config.LegacyStateDirName, err)
	}
}

// TestNoResolvableStateDir: with no override, no XDG_STATE_HOME and no HOME,
// the state dir cannot be resolved — ResolveBase errors and the read-only
// probes degrade cleanly (RunEnvExists false, OpenBase a clean nil/nil).
func TestNoResolvableStateDir(t *testing.T) {
	t.Setenv("SANDBOXER_STATE", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "")
	src := t.TempDir()
	if _, err := ResolveBase(src); err == nil {
		t.Error("ResolveBase with no resolvable state dir should error")
	}
	if RunEnvExists(src) {
		t.Error("RunEnvExists should be false when no state dir resolves")
	}
	if b, err := OpenBase(src); b != nil || err != nil {
		t.Errorf("OpenBase with no state dir = (%v, %v), want (nil, nil)", b, err)
	}
}

func TestResolveBaseMissing(t *testing.T) {
	if _, err := ResolveBase(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("ResolveBase of missing dir should error")
	}
	f := filepath.Join(t.TempDir(), "file")
	writeFile(t, f, "x")
	if _, err := ResolveBase(f); err == nil {
		t.Error("ResolveBase of a file (not dir) should error")
	}
}

func TestPathHelpers(t *testing.T) {
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pid := filepath.Base(b.Dir)
	cases := []struct{ got, suffix string }{
		{b.SandboxDir("s"), filepath.Join(pid, "s")},
		{b.ProfileJSONPath("s"), filepath.Join("_meta", "s.profile.json")},
		{b.MetaFilePath("s"), filepath.Join("_meta", "s.meta")},
		{b.LogPath("s", "json"), filepath.Join("_logs", "s.json")},
		{b.AgentsListPath(), filepath.Join("_meta", "agents.list")},
	}
	for _, c := range cases {
		if !strings.HasSuffix(c.got, c.suffix) {
			t.Errorf("path %q does not end with %q", c.got, c.suffix)
		}
	}
}

func TestAgentsAppendRemove(t *testing.T) {
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if a := b.Agents(); len(a) != 0 {
		t.Errorf("fresh base has agents: %v", a)
	}
	for _, s := range []string{"one", "two", "one"} {
		if err := b.AppendAgent(s); err != nil {
			t.Fatal(err)
		}
	}
	if a := b.Agents(); len(a) != 2 || a[0] != "one" || a[1] != "two" {
		t.Errorf("Agents = %v, want [one two]", a)
	}
	if err := b.RemoveAgent("one"); err != nil {
		t.Fatal(err)
	}
	if a := b.Agents(); len(a) != 1 || a[0] != "two" {
		t.Errorf("after remove Agents = %v, want [two]", a)
	}
}

func TestCurrent(t *testing.T) {
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cur := b.Current(); cur != "" {
		t.Errorf("fresh Current = %q, want empty", cur)
	}
	if err := b.ClearCurrent(); err != nil {
		t.Errorf("ClearCurrent on empty: %v", err)
	}
	if err := b.SetCurrent("feat"); err != nil {
		t.Fatal(err)
	}
	if cur := b.Current(); cur != "feat" {
		t.Errorf("Current = %q, want feat", cur)
	}
	if err := b.ClearCurrent(); err != nil {
		t.Fatal(err)
	}
	if cur := b.Current(); cur != "" {
		t.Errorf("after clear Current = %q, want empty", cur)
	}
}

func TestSetDomains(t *testing.T) {
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.SetDomains("a.com,b.com"); err != nil {
		t.Fatal(err)
	}
	if b.Domains != "a.com,b.com" {
		t.Errorf("in-memory Domains = %q", b.Domains)
	}
	data, _ := os.ReadFile(filepath.Join(b.metaDir(), "run.env"))
	s := string(data)
	if !strings.Contains(s, "DOMAINS=a.com,b.com") || !strings.Contains(s, "SRC=") {
		t.Errorf("run.env unexpected:\n%s", s)
	}
}

func TestWriteProfileJSON(t *testing.T) {
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WriteProfileJSON("s", []byte(`{"name":"s"}`)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(b.ProfileJSONPath("s"))
	if err != nil || string(data) != `{"name":"s"}` {
		t.Errorf("profile.json = %q, err=%v", data, err)
	}
}

func TestRemove(t *testing.T) {
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(b.SandboxDir("s"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, b.MetaFilePath("s"), "exit=0\n")
	writeFile(t, b.LogPath("s", "json"), "{}")
	if err := b.AppendAgent("s"); err != nil {
		t.Fatal(err)
	}
	if err := b.SetCurrent("s"); err != nil {
		t.Fatal(err)
	}
	if err := b.Remove("s"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	for _, p := range []string{b.SandboxDir("s"), b.MetaFilePath("s"), b.LogPath("s", "json")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("path still present after Remove: %s", p)
		}
	}
	if len(b.Agents()) != 0 {
		t.Errorf("agent not removed: %v", b.Agents())
	}
	if b.Current() != "" {
		t.Errorf("current not cleared: %q", b.Current())
	}
}

func TestRemoveKeepsOtherCurrent(t *testing.T) {
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.AppendAgent("a"); err != nil {
		t.Fatal(err)
	}
	if err := b.SetCurrent("b"); err != nil {
		t.Fatal(err)
	}
	if err := b.Remove("a"); err != nil {
		t.Fatal(err)
	}
	if b.Current() != "b" {
		t.Errorf("removing a non-current sandbox cleared current: %q", b.Current())
	}
}

func TestAgentsIgnoresBlankLines(t *testing.T) {
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.AgentsListPath(), []byte("one\n\n  \ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := b.Agents(); len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Errorf("Agents = %v, want [one two]", got)
	}
}

func TestParseEnvFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "e")
	writeFile(t, p, "A=1\nB=two=three\nnoequalsline\nC=\n")
	env, err := parseEnvFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if env["A"] != "1" || env["B"] != "two=three" || env["C"] != "" {
		t.Errorf("parsed env = %v", env)
	}
	if _, ok := env["noequalsline"]; ok {
		t.Error("line without '=' should be ignored")
	}
	if _, err := parseEnvFile(filepath.Join(dir, "missing")); err == nil {
		t.Error("parseEnvFile of missing file should error")
	}
}

// TestMakeSandboxRejectsNonGit: no profile means no srcs — MakeSandbox refuses
// with the explicit srcs-is-empty guidance (there is no implicit current-dir
// default); an explicit non-git src gets the git-only rejection.
func TestMakeSandboxRejectsNonGit(t *testing.T) {
	b, err := ResolveBase(t.TempDir()) // a bare temp dir is not a git repo
	if err != nil {
		t.Fatal(err)
	}
	err = b.MakeSandbox("feat", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "srcs is empty") {
		t.Errorf("MakeSandbox without srcs = %v, want the srcs-is-empty explanation", err)
	}
	if err := b.WriteProfileJSON("feat", []byte(`{"srcs":[{"src":"."}]}`)); err != nil {
		t.Fatal(err)
	}
	err = b.MakeSandbox("feat", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("MakeSandbox on a non-git src = %v, want the git-only rejection", err)
	}
	if _, err := os.Stat(b.SandboxDir("feat")); !os.IsNotExist(err) {
		t.Error("no sandbox dir should be created for a rejected non-git project")
	}
}
