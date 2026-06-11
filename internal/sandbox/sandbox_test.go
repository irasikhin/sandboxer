package sandbox

import (
	"io"
	"os"
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

// TestResolveBaseGitignoreAllowlist pins the .sandboxer/.gitignore body: an
// allowlist that commits only the .gitignore, config.yaml and image.nix while
// the leading "*" keeps the generated state (_meta/_home/_logs/<slug>) ignored —
// the credential-leak guard.
func TestResolveBaseGitignoreAllowlist(t *testing.T) {
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatalf("ResolveBase: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(b.Dir, ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	want := "*\n!.gitignore\n!config.yaml\n!image.nix\n"
	if string(got) != want {
		t.Errorf(".gitignore = %q, want %q", got, want)
	}
	// The leading "*" still ignores the state dirs — those names are NOT in the
	// allowlist, so the credential-leak guard is intact.
	for _, ignored := range []string{"_meta", "_home", "_logs"} {
		if strings.Contains(string(got), "!"+ignored) {
			t.Errorf(".gitignore must not un-ignore %s:\n%s", ignored, got)
		}
	}
}

// TestResolveBaseUpgradesBlanketGitignore pins the migration: an old sandbox's
// blanket "*\n" .gitignore is upgraded to the allowlist on the next ResolveBase,
// so a committed config.yaml/image.nix stops being ignored. A user-customized
// gitignore is left untouched.
func TestResolveBaseUpgradesBlanketGitignore(t *testing.T) {
	allow := "*\n!.gitignore\n!config.yaml\n!image.nix\n"

	// Blanket "*\n" → upgraded to the allowlist.
	src := t.TempDir()
	gi := filepath.Join(src, config.StateDirName, ".gitignore")
	writeFile(t, gi, "*\n")
	if _, err := ResolveBase(src); err != nil {
		t.Fatalf("ResolveBase: %v", err)
	}
	if got, _ := os.ReadFile(gi); string(got) != allow {
		t.Errorf("blanket gitignore not upgraded: %q", got)
	}

	// A user-customized gitignore is preserved as-is.
	src2 := t.TempDir()
	gi2 := filepath.Join(src2, config.StateDirName, ".gitignore")
	custom := "*\n!keep-me\n"
	writeFile(t, gi2, custom)
	if _, err := ResolveBase(src2); err != nil {
		t.Fatalf("ResolveBase: %v", err)
	}
	if got, _ := os.ReadFile(gi2); string(got) != custom {
		t.Errorf("customized gitignore was overwritten: %q", got)
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
	writeFile(t, b.ManifestPath("s"), "[]")
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
	for _, p := range []string{b.SandboxDir("s"), b.MetaFilePath("s"), b.ManifestPath("s"), b.LogPath("s", "json")} {
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

// TestPullDepsCorruptProfile: a stored profile.json that won't parse surfaces as
// a wrapped pull error (rather than a silent no-op), so a refresh-and-pull fails
// loudly.
func TestPullDepsCorruptProfile(t *testing.T) {
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.WriteProfileJSON("feat", []byte("not json")); err != nil {
		t.Fatal(err)
	}
	if err := b.PullDeps("feat", io.Discard); err == nil {
		t.Error("PullDeps with a corrupt profile should error")
	}
}

// TestMakeSandboxEmpty: with no profile, nothing is copied — just the empty
// sandbox dir and registration.
func TestMakeSandboxEmpty(t *testing.T) {
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("feat", io.Discard); err != nil {
		t.Fatalf("MakeSandbox: %v", err)
	}
	dest := b.SandboxDir("feat")
	if fi, err := os.Stat(dest); err != nil || !fi.IsDir() {
		t.Errorf("sandbox dir not created: %v", err)
	}
	entries, _ := os.ReadDir(dest)
	if len(entries) != 0 {
		t.Errorf("sandbox should be empty without srcs, has %d entries", len(entries))
	}
	if _, err := os.Stat(b.ManifestPath("feat")); !os.IsNotExist(err) {
		t.Error("no manifest should be written when there is no profile")
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
