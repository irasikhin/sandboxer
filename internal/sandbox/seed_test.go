package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plantHostConfigs builds a fake host home with the agent configs a seed run
// should pick up — and the private bulk it must leave behind.
func plantHostConfigs(t *testing.T) {
	t.Helper()
	host := t.TempDir()
	t.Setenv("HOME", host)
	writeFile(t, filepath.Join(host, ".claude", ".credentials.json"), "token")
	if err := os.Chmod(filepath.Join(host, ".claude", ".credentials.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(host, ".claude", "settings.json"), "{}")
	writeFile(t, filepath.Join(host, ".claude", "projects", "p", "chat.jsonl"), "transcript")
	writeFile(t, filepath.Join(host, ".claude.json"), `{"state":1}`)
	if err := os.Symlink("settings.json", filepath.Join(host, ".claude", "link")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(host, ".config", "opencode", "opencode.json"), "oc")
}

// TestSeedHomeCopiesAgentConfigs: the registry seed paths land in the sandbox
// home — credentials with their modes, nested config dirs, symlinks as links —
// while skip-listed bulk (transcripts) stays behind.
func TestSeedHomeCopiesAgentConfigs(t *testing.T) {
	plantHostConfigs(t)
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.EnsureHome("s"); err != nil {
		t.Fatal(err)
	}
	var progress strings.Builder
	b.SeedHome("s", &progress)
	home := b.HomeDir("s")

	cred := filepath.Join(home, ".claude", ".credentials.json")
	data, err := os.ReadFile(cred)
	if err != nil || string(data) != "token" {
		t.Fatalf("credentials not seeded: %q err=%v", data, err)
	}
	if fi, err := os.Stat(cred); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("credentials mode = %v (err=%v), want 0600 preserved", fi.Mode(), err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); err != nil {
		t.Errorf(".claude.json not seeded: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "opencode", "opencode.json")); err != nil {
		t.Errorf("nested opencode config not seeded: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "projects")); !os.IsNotExist(err) {
		t.Errorf("skip-listed projects/ was seeded (err=%v)", err)
	}
	if target, err := os.Readlink(filepath.Join(home, ".claude", "link")); err != nil || target != "settings.json" {
		t.Errorf("symlink not recreated as a link: %q err=%v", target, err)
	}
	if got := progress.String(); !strings.Contains(got, "claude: host ~/.claude seeded") {
		t.Errorf("no seed notice, got %q", got)
	}
	if strings.Contains(progress.String(), "skipped") {
		t.Errorf("unexpected seed failure: %q", progress.String())
	}
}

// TestSeedHomeNeverOverwrites: a path already present in the sandbox home —
// an in-sandbox login, logout or edit — survives every later seed untouched.
func TestSeedHomeNeverOverwrites(t *testing.T) {
	plantHostConfigs(t)
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.EnsureHome("s"); err != nil {
		t.Fatal(err)
	}
	home := b.HomeDir("s")
	writeFile(t, filepath.Join(home, ".claude.json"), "sandbox-edited")
	b.SeedHome("s", nil)
	if data, _ := os.ReadFile(filepath.Join(home, ".claude.json")); string(data) != "sandbox-edited" {
		t.Errorf(".claude.json overwritten by seed: %q", data)
	}
	// a second run is a no-op for the now-present paths too
	writeFile(t, filepath.Join(home, ".claude", "wip.txt"), "keep")
	b.SeedHome("s", nil)
	if _, err := os.Stat(filepath.Join(home, ".claude", "wip.txt")); err != nil {
		t.Errorf("in-sandbox file lost on re-seed: %v", err)
	}
}

// TestSeedHomeUnreadableSkips: a host config that cannot be read warns and is
// skipped — never a failed enter, never a half-seeded path left behind.
func TestSeedHomeUnreadableSkips(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads anything — the permission trap needs an unprivileged user")
	}
	host := t.TempDir()
	t.Setenv("HOME", host)
	writeFile(t, filepath.Join(host, ".gemini", "oauth_creds.json"), "secret")
	if err := os.Chmod(filepath.Join(host, ".gemini", "oauth_creds.json"), 0o000); err != nil {
		t.Fatal(err)
	}
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.EnsureHome("s"); err != nil {
		t.Fatal(err)
	}
	var progress strings.Builder
	b.SeedHome("s", &progress)
	if !strings.Contains(progress.String(), "skipped") {
		t.Errorf("no skip warning for an unreadable config: %q", progress.String())
	}
	home := b.HomeDir("s")
	if _, err := os.Stat(filepath.Join(home, ".gemini")); !os.IsNotExist(err) {
		t.Errorf("half-seeded .gemini left behind (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini.seedtmp")); !os.IsNotExist(err) {
		t.Errorf("staging dir left behind (err=%v)", err)
	}
}

// TestSeedHomeEmptyHost: nothing on the host means nothing to do — quietly.
func TestSeedHomeEmptyHost(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.EnsureHome("s"); err != nil {
		t.Fatal(err)
	}
	var progress strings.Builder
	b.SeedHome("s", &progress)
	if progress.Len() != 0 {
		t.Errorf("seed on an empty host home printed %q, want silence", progress.String())
	}
	entries, err := os.ReadDir(b.HomeDir("s"))
	if err != nil || len(entries) != 0 {
		t.Errorf("sandbox home not left empty: %v err=%v", entries, err)
	}
}
