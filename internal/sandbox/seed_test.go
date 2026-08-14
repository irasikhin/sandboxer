package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// plantHostConfigs builds a fake host home with the agent configs a seed run
// should pick up — and the private bulk it must leave behind.
func plantHostConfigs(t *testing.T) {
	t.Helper()
	host := t.TempDir()
	t.Setenv("HOME", host)
	writeFile(t, filepath.Join(host, ".claude", ".credentials.json"), "rotating-oauth")
	writeFile(t, filepath.Join(host, ".claude", "settings.json"), "{}")
	if err := os.Chmod(filepath.Join(host, ".claude", "settings.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(host, ".claude", "skills", "deploy", "SKILL.md"), "skill")
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

	settings := filepath.Join(home, ".claude", "settings.json")
	data, err := os.ReadFile(settings)
	if err != nil || string(data) != "{}" {
		t.Fatalf("settings not seeded: %q err=%v", data, err)
	}
	if fi, err := os.Stat(settings); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("settings mode = %v (err=%v), want 0600 preserved", fi.Mode(), err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "deploy", "SKILL.md")); err != nil {
		t.Errorf("skills not seeded: %v", err)
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
	// the rotating OAuth pair NEVER travels: a copy dies on the next refresh
	// either side performs — long-lived tokens go via authEnv instead
	if _, err := os.Stat(filepath.Join(home, ".claude", ".credentials.json")); !os.IsNotExist(err) {
		t.Errorf(".credentials.json was seeded, want excluded (err=%v)", err)
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

// TestSeedHomeUnreadableSkips: a host file that cannot be read warns and is
// skipped — never a failed enter, never a torn file or staging leftover, and
// one bad file must not strand its readable siblings.
func TestSeedHomeUnreadableSkips(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads anything — the permission trap needs an unprivileged user")
	}
	host := t.TempDir()
	t.Setenv("HOME", host)
	writeFile(t, filepath.Join(host, ".gemini", "oauth_creds.json"), "secret")
	writeFile(t, filepath.Join(host, ".gemini", "settings.json"), "{}")
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
	if _, err := os.Stat(filepath.Join(home, ".gemini", "settings.json")); err != nil {
		t.Errorf("readable sibling not seeded: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini", "oauth_creds.json")); !os.IsNotExist(err) {
		t.Errorf("unreadable file materialized somehow (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini", "oauth_creds.json.seedtmp")); !os.IsNotExist(err) {
		t.Errorf("staging file left behind (err=%v)", err)
	}
}

// TestSeedHomeMergesIntoExistingHome pins the fix for the first-release trap:
// a sandbox that already RAN an agent has ~/.claude (created on first launch,
// no credentials inside), and the seed must still deliver the missing files —
// per-file merge, not a whole-dir "exists, skip".
func TestSeedHomeMergesIntoExistingHome(t *testing.T) {
	plantHostConfigs(t)
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.EnsureHome("s"); err != nil {
		t.Fatal(err)
	}
	home := b.HomeDir("s")
	// the sandbox's own prior agent run: .claude exists with its own state
	writeFile(t, filepath.Join(home, ".claude", "statsig", "cache"), "sandbox-local")
	writeFile(t, filepath.Join(home, ".claude", "settings.json"), `{"sandbox":true}`)

	var progress strings.Builder
	b.SeedHome("s", &progress)

	// missing files arrived…
	if data, err := os.ReadFile(filepath.Join(home, ".claude", "skills", "deploy", "SKILL.md")); err != nil || string(data) != "skill" {
		t.Fatalf("host skills not merged into the existing .claude: %q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); err != nil {
		t.Errorf(".claude.json not seeded: %v", err)
	}
	// …the sandbox's own files stayed exactly as they were…
	if data, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json")); string(data) != `{"sandbox":true}` {
		t.Errorf("existing settings.json overwritten: %q", data)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "statsig", "cache")); err != nil {
		t.Errorf("sandbox-local file lost in merge: %v", err)
	}
	// …and the notice reports what was added
	if !strings.Contains(progress.String(), "claude: host ~/.claude seeded") {
		t.Errorf("no merge notice, got %q", progress.String())
	}
}

// TestSeedHomeShadowedSubtree: where the sandbox home holds a FILE at a path
// the host has as a DIRECTORY (or an own symlink), the sandbox's version wins
// and the host subtree stays out — merge adds, it never reshapes.
func TestSeedHomeShadowedSubtree(t *testing.T) {
	host := t.TempDir()
	t.Setenv("HOME", host)
	writeFile(t, filepath.Join(host, ".gemini", "commands", "deploy.toml"), "host")
	writeFile(t, filepath.Join(host, ".gemini", "GEMINI.md"), "host-memory")
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.EnsureHome("s"); err != nil {
		t.Fatal(err)
	}
	home := b.HomeDir("s")
	// the sandbox made "commands" a FILE and GEMINI.md a symlink of its own
	writeFile(t, filepath.Join(home, ".gemini", "commands"), "sandbox-file")
	if err := os.Symlink("elsewhere", filepath.Join(home, ".gemini", "GEMINI.md")); err != nil {
		t.Fatal(err)
	}

	b.SeedHome("s", nil)

	if data, _ := os.ReadFile(filepath.Join(home, ".gemini", "commands")); string(data) != "sandbox-file" {
		t.Errorf("sandbox file shadowing a host dir was replaced: %q", data)
	}
	if target, err := os.Readlink(filepath.Join(home, ".gemini", "GEMINI.md")); err != nil || target != "elsewhere" {
		t.Errorf("sandbox symlink overwritten by seed: %q err=%v", target, err)
	}
}

// TestSeedMergeErrorPaths: failures to materialize one entry warn and never
// abort the rest — a read-only sandbox dir and a stale .seedtmp in the way
// are reported, nothing torn is left behind.
func TestSeedMergeErrorPaths(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root writes anywhere — the permission traps need an unprivileged user")
	}
	host := t.TempDir()
	t.Setenv("HOME", host)
	writeFile(t, filepath.Join(host, ".gemini", "sub", "creds.json"), "x")
	writeFile(t, filepath.Join(host, ".claude.json"), "cfg")
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.EnsureHome("s"); err != nil {
		t.Fatal(err)
	}
	home := b.HomeDir("s")
	// a stale .seedtmp DIRECTORY sits where the file copy stages
	if err := os.MkdirAll(filepath.Join(home, ".claude.json.seedtmp", "junk"), 0o755); err != nil {
		t.Fatal(err)
	}
	// and the sandbox's .gemini is read-only, so sub/ cannot be created
	if err := os.MkdirAll(filepath.Join(home, ".gemini"), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(home, ".gemini"), 0o700) })

	var progress strings.Builder
	b.SeedHome("s", &progress)
	if !strings.Contains(progress.String(), "skipped") {
		t.Errorf("no warning for failed seeds: %q", progress.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
		t.Errorf(".claude.json materialized despite a blocked staging path (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".gemini", "sub")); !os.IsNotExist(err) {
		t.Errorf("sub/ materialized inside a read-only dir (err=%v)", err)
	}
}

// TestSeedMergeNestedFileCreatesParents: a seed path that points straight at a
// nested file (opencode's .local/share/opencode/auth.json) is copied even when
// none of its parent dirs exist in the sandbox home yet — the bug that made the
// seed warn "open …auth.json.seedtmp: no such file or directory".
func TestSeedMergeNestedFileCreatesParents(t *testing.T) {
	srcFile := filepath.Join(t.TempDir(), "auth.json")
	writeFile(t, srcFile, "token")
	dst := filepath.Join(t.TempDir(), "home", ".local", "share", "opencode", "auth.json")

	n, err := seedMerge(srcFile, dst, nil)
	if err != nil {
		t.Fatalf("seedMerge nested file: %v", err)
	}
	if n != 1 {
		t.Errorf("added = %d, want 1", n)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "token" {
		t.Errorf("copied file = %q, %v; want token", got, err)
	}
	if _, err := os.Stat(dst + ".seedtmp"); !os.IsNotExist(err) {
		t.Errorf("staging temp left behind: %v", err)
	}
}

// TestSeedHomeNoHostHome: an unresolvable host home is a quiet no-op.
func TestSeedHomeNoHostHome(t *testing.T) {
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.EnsureHome("s"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", "")
	var progress strings.Builder
	b.SeedHome("s", &progress)
	if progress.Len() != 0 {
		t.Errorf("seed without a host home printed %q, want silence", progress.String())
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
	// Snapshot AFTER EnsureHome: the invariant under test is that SeedHome
	// copies nothing when the host has nothing, not that the home is empty —
	// EnsureHome legitimately seeds its own ~/.tmux.conf, which is not a host
	// config and must not be confused for one.
	before := dirNames(t, b.HomeDir("s"))

	var progress strings.Builder
	b.SeedHome("s", &progress)
	if progress.Len() != 0 {
		t.Errorf("seed on an empty host home printed %q, want silence", progress.String())
	}
	if after := dirNames(t, b.HomeDir("s")); !slices.Equal(before, after) {
		t.Errorf("SeedHome invented files with nothing on the host: %v -> %v", before, after)
	}
}

// dirNames lists a directory's entry names, sorted.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}
