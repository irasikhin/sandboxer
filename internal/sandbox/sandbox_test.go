package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/gitx"
)

// isolateGit keeps git's identity deterministic (see gitx tests).
func isolateGit(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "global"))
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(t.TempDir(), "system"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
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
	isolateGit(t)
	src := t.TempDir() // not a git repo

	if RunEnvExists(src) {
		t.Error("RunEnvExists should be false before ResolveBase")
	}
	b, err := ResolveBase(src)
	if err != nil {
		t.Fatalf("ResolveBase: %v", err)
	}
	if b.IsGit {
		t.Error("non-git dir should yield IsGit=false")
	}
	if !RunEnvExists(src) {
		t.Error("RunEnvExists should be true after ResolveBase")
	}
	// run.env and agents.list seeded.
	if _, err := os.Stat(filepath.Join(b.metaDir(), "run.env")); err != nil {
		t.Errorf("run.env not seeded: %v", err)
	}
	if _, err := os.Stat(b.AgentsListPath()); err != nil {
		t.Errorf("agents.list not seeded: %v", err)
	}
	// Defaults flow through to the loaded base.
	if b.Domains != config.DefaultDomains {
		t.Errorf("Domains = %q, want defaults", b.Domains)
	}
	// Re-resolve must not error or clobber.
	if _, err := ResolveBase(src); err != nil {
		t.Fatalf("re-ResolveBase: %v", err)
	}
}

func TestResolveBaseGitRepo(t *testing.T) {
	isolateGit(t)
	src := t.TempDir()
	if err := gitx.Init(src); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(src, "f.txt"), "x\n")
	if err := gitx.Snapshot(src, "init"); err != nil {
		t.Fatal(err)
	}
	want := gitx.HeadSHA(src)

	b, err := ResolveBase(src)
	if err != nil {
		t.Fatalf("ResolveBase: %v", err)
	}
	if !b.IsGit {
		t.Error("git repo should yield IsGit=true")
	}
	if b.BaseSHA != want {
		t.Errorf("BaseSHA = %q, want %q", b.BaseSHA, want)
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
	isolateGit(t)
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		got, suffix string
	}{
		{b.SandboxDir("s"), filepath.Join(".sandboxer", "s")},
		{b.ProfileJSONPath("s"), filepath.Join("_meta", "s.profile.json")},
		{b.ManifestPath("s"), filepath.Join("_meta", "s.manifest.json")},
		{b.BaseFilePath("s"), filepath.Join("_meta", "s.base")},
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
	isolateGit(t)
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if a := b.Agents(); len(a) != 0 {
		t.Errorf("fresh base has agents: %v", a)
	}
	for _, s := range []string{"one", "two", "one"} { // duplicate ignored
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
	isolateGit(t)
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cur := b.Current(); cur != "" {
		t.Errorf("fresh Current = %q, want empty", cur)
	}
	if err := b.ClearCurrent(); err != nil { // clearing when unset is fine
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
	isolateGit(t)
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
	if !strings.Contains(s, "DOMAINS=a.com,b.com") {
		t.Errorf("run.env missing new DOMAINS:\n%s", s)
	}
	if !strings.Contains(s, "SRC=") || !strings.Contains(s, "IS_GIT=") {
		t.Errorf("run.env lost other keys:\n%s", s)
	}
}

func TestWriteProfileJSON(t *testing.T) {
	isolateGit(t)
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

func TestBaseRef(t *testing.T) {
	isolateGit(t)
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if ref := b.BaseRef("s", "fallback-sha"); ref != "fallback-sha" {
		t.Errorf("BaseRef no file = %q, want fallback", ref)
	}
	if ref := b.BaseRef("s", ""); ref != "HEAD" {
		t.Errorf("BaseRef empty fallback = %q, want HEAD", ref)
	}
	writeFile(t, b.BaseFilePath("s"), "deadbeef\n")
	if ref := b.BaseRef("s", "fallback"); ref != "deadbeef" {
		t.Errorf("BaseRef with file = %q, want deadbeef", ref)
	}
}

func TestRemove(t *testing.T) {
	isolateGit(t)
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(b.SandboxDir("s"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, b.MetaFilePath("s"), "exit=0\n")
	writeFile(t, b.BaseFilePath("s"), "sha\n")
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
	for _, p := range []string{b.SandboxDir("s"), b.MetaFilePath("s"), b.BaseFilePath("s"), b.LogPath("s", "json")} {
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

func requireExec(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		if _, err := exec.LookPath(n); err != nil {
			t.Skipf("%s not available", n)
		}
	}
}

func TestMakeSandboxNonGit(t *testing.T) {
	requireExec(t, "rsync", "git")
	isolateGit(t)
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "hello.txt"), "hi\n")

	b, err := ResolveBase(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("feat", os.Stderr); err != nil {
		t.Fatalf("MakeSandbox: %v", err)
	}
	dest := b.SandboxDir("feat")
	if _, err := os.Stat(filepath.Join(dest, "hello.txt")); err != nil {
		t.Errorf("project file not copied into sandbox: %v", err)
	}
	// .sandboxer state must be excluded from the copy.
	if _, err := os.Stat(filepath.Join(dest, config.StateDirName)); !os.IsNotExist(err) {
		t.Error(".sandboxer should be excluded from the rsync copy")
	}
	// A non-git project gets a fresh repo + snapshot branch.
	if !gitx.IsRepo(dest) {
		t.Error("sandbox copy should be a git repo")
	}
	if br, _ := gitx.CurrentBranch(dest); br != "sandbox/feat" {
		t.Errorf("branch = %q, want sandbox/feat", br)
	}
	// Registered and base recorded.
	found := false
	for _, a := range b.Agents() {
		if a == "feat" {
			found = true
		}
	}
	if !found {
		t.Errorf("sandbox not registered: %v", b.Agents())
	}
	if _, err := os.Stat(b.BaseFilePath("feat")); err != nil {
		t.Errorf("base file not written: %v", err)
	}
}

func TestMakeSandboxAndCommitWorkGit(t *testing.T) {
	requireExec(t, "rsync", "git")
	isolateGit(t)
	src := t.TempDir()
	if err := gitx.Init(src); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(src, "f.txt"), "v1\n")
	if err := gitx.Snapshot(src, "init"); err != nil {
		t.Fatal(err)
	}

	b, err := ResolveBase(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("feat", os.Stderr); err != nil {
		t.Fatalf("MakeSandbox: %v", err)
	}
	dest := b.SandboxDir("feat")
	before := gitx.HeadSHA(dest)

	// Change the copy and commit the work.
	writeFile(t, filepath.Join(dest, "f.txt"), "v2\n")
	if err := b.CommitWork("feat"); err != nil {
		t.Fatalf("CommitWork: %v", err)
	}
	after := gitx.HeadSHA(dest)
	if before == "" || after == "" || before == after {
		t.Errorf("CommitWork did not create a new commit (before=%q after=%q)", before, after)
	}
}
