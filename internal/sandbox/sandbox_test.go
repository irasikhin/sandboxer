package sandbox

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
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
	// run.env carries no git fields anymore.
	data, _ := os.ReadFile(filepath.Join(b.metaDir(), "run.env"))
	if strings.Contains(string(data), "IS_GIT") || strings.Contains(string(data), "BASE_SHA") {
		t.Errorf("run.env should not contain git fields:\n%s", data)
	}
	if _, err := ResolveBase(src); err != nil {
		t.Fatalf("re-ResolveBase: %v", err)
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
	cases := []struct{ got, suffix string }{
		{b.SandboxDir("s"), filepath.Join(".sandboxer", "s")},
		{b.ProfileJSONPath("s"), filepath.Join("_meta", "s.profile.json")},
		{b.ManifestPath("s"), filepath.Join("_meta", "s.manifest.json")},
		{b.baselinePath("s"), filepath.Join("_meta", "s.baseline.json")},
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
	writeFile(t, b.baselinePath("s"), "{}")
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
	for _, p := range []string{b.SandboxDir("s"), b.MetaFilePath("s"), b.baselinePath("s"), b.LogPath("s", "json")} {
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

// --- copy + baseline + return -----------------------------------------------

func TestMakeSandboxCopiesAndExcludes(t *testing.T) {
	requireExec(t, "rsync")
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "hello.txt"), "hi\n")
	writeFile(t, filepath.Join(src, ".git", "config"), "[core]\n") // must be excluded

	b, err := ResolveBase(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("feat", io.Discard); err != nil {
		t.Fatalf("MakeSandbox: %v", err)
	}
	dest := b.SandboxDir("feat")
	if _, err := os.Stat(filepath.Join(dest, "hello.txt")); err != nil {
		t.Errorf("project file not copied: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, config.StateDirName)); !os.IsNotExist(err) {
		t.Error(".sandboxer must be excluded from the copy")
	}
	if _, err := os.Stat(filepath.Join(dest, ".git")); !os.IsNotExist(err) {
		t.Error(".git must be excluded from the copy")
	}
	if _, err := os.Stat(b.baselinePath("feat")); err != nil {
		t.Errorf("baseline not written: %v", err)
	}
	found := false
	for _, a := range b.Agents() {
		if a == "feat" {
			found = true
		}
	}
	if !found {
		t.Errorf("sandbox not registered: %v", b.Agents())
	}
}

func TestChangedFilesAndReturn(t *testing.T) {
	requireExec(t, "rsync")
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "app.txt"), "v1\n")
	b, err := ResolveBase(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("feat", io.Discard); err != nil {
		t.Fatal(err)
	}
	dest := b.SandboxDir("feat")

	if n := b.ChangedFiles("feat"); n != 0 {
		t.Errorf("fresh copy ChangedFiles = %d, want 0", n)
	}

	// Agent edits a file and adds a new one inside the copy.
	writeFile(t, filepath.Join(dest, "app.txt"), "v2-changed\n")
	writeFile(t, filepath.Join(dest, "new.txt"), "added\n")
	if n := b.ChangedFiles("feat"); n != 2 {
		t.Errorf("ChangedFiles after edits = %d, want 2", n)
	}

	var buf bytes.Buffer
	if err := b.Return("feat", false, &buf); err != nil {
		t.Fatalf("Return: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(src, "app.txt")); string(got) != "v2-changed\n" {
		t.Errorf("source app.txt after return = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(src, "new.txt")); string(got) != "added\n" {
		t.Errorf("source new.txt after return = %q", got)
	}
	if !strings.Contains(buf.String(), "RETURN app.txt") {
		t.Errorf("return output: %q", buf.String())
	}
}

func TestReturnSkipAndForce(t *testing.T) {
	requireExec(t, "rsync")
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "app.txt"), "v1\n")
	b, err := ResolveBase(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("feat", io.Discard); err != nil {
		t.Fatal(err)
	}
	dest := b.SandboxDir("feat")

	// Agent edits the copy; the source is also edited out-of-band after create.
	writeFile(t, filepath.Join(dest, "app.txt"), "from-sandbox\n")
	writeFile(t, filepath.Join(src, "app.txt"), "external-change\n")

	var buf bytes.Buffer
	if err := b.Return("feat", false, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "SKIP") {
		t.Errorf("expected SKIP, got: %q", buf.String())
	}
	if got, _ := os.ReadFile(filepath.Join(src, "app.txt")); string(got) != "external-change\n" {
		t.Errorf("source overwritten despite SKIP: %q", got)
	}

	buf.Reset()
	if err := b.Return("feat", true, &buf); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(src, "app.txt")); string(got) != "from-sandbox\n" {
		t.Errorf("source after --force = %q, want from-sandbox", got)
	}
}

func TestDiff(t *testing.T) {
	requireExec(t, "rsync", "diff")
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "app.txt"), "original\n")
	b, err := ResolveBase(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("feat", io.Discard); err != nil {
		t.Fatal(err)
	}
	if d, _ := b.Diff("feat"); d != "" {
		t.Errorf("fresh copy diff should be empty, got: %q", d)
	}
	writeFile(t, filepath.Join(b.SandboxDir("feat"), "app.txt"), "edited\n")
	d, err := b.Diff("feat")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d, "edited") || !strings.Contains(d, "original") {
		t.Errorf("diff missing change markers:\n%s", d)
	}
}

func TestDiffNewFile(t *testing.T) {
	requireExec(t, "rsync", "diff")
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "keep.txt"), "k\n")
	b, err := ResolveBase(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("feat", io.Discard); err != nil {
		t.Fatal(err)
	}
	// A brand-new file in the copy → diff against /dev/null.
	writeFile(t, filepath.Join(b.SandboxDir("feat"), "added.txt"), "brand new\n")
	d, _ := b.Diff("feat")
	if !strings.Contains(d, "brand new") {
		t.Errorf("diff of a new file missing its content:\n%s", d)
	}
}

func TestFileSig(t *testing.T) {
	if fileSig(filepath.Join(t.TempDir(), "missing")) != "" {
		t.Error("missing path sig should be empty")
	}
	if fileSig(t.TempDir()) != "" {
		t.Error("directory sig should be empty")
	}
	f := filepath.Join(t.TempDir(), "f")
	writeFile(t, f, "data")
	if !strings.Contains(fileSig(f), ":") {
		t.Errorf("file sig = %q, want mtime:size", fileSig(f))
	}
}

func TestWalkFilesSkips(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "keep.txt"), "k")
	writeFile(t, filepath.Join(root, "node_modules", "x.js"), "n")
	writeFile(t, filepath.Join(root, ".git", "config"), "g")
	writeFile(t, filepath.Join(root, "sandboxer.tasks"), "t")
	var got []string
	walkFiles(root, func(rel, _ string) { got = append(got, rel) })
	if len(got) != 1 || got[0] != "keep.txt" {
		t.Errorf("walkFiles visited %v, want only keep.txt", got)
	}
}
