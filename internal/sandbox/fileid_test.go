//go:build unix

package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStatIdentityRealFile: a real path yields a non-empty identity, stable for
// the same inode object and unchanged when only the directory's CONTENTS change
// (an inode's device/number/birth-time are all immutable) — the property that
// keeps a live session from rebuilding every time the agent writes a file.
func TestStatIdentityRealFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	id, ok := statIdentity(dir)
	if !ok || id == "" {
		t.Fatalf("statIdentity(dir) = (%q, %v), want a real identity", id, ok)
	}
	if !strings.Contains(id, ":") {
		t.Errorf("identity = %q, want a dev:inode[:btime] form", id)
	}

	// adding a file changes the directory's contents (and its ctime/mtime) but
	// NOT its object identity — the fingerprint must not move.
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if again, _ := statIdentity(dir); again != id {
		t.Errorf("identity changed on an in-dir write (same inode): %q → %q", id, again)
	}
}

// TestStatIdentityMissing: a path that cannot be stat'd reports not-ok, which
// inodeID turns into its "missing" sentinel.
func TestStatIdentityMissing(t *testing.T) {
	if id, ok := statIdentity(filepath.Join(t.TempDir(), "nope")); ok {
		t.Errorf("statIdentity(absent) = (%q, true), want not-ok", id)
	}
	if got := inodeID(filepath.Join(t.TempDir(), "nope")); got != "missing" {
		t.Errorf("inodeID(absent) = %q, want \"missing\"", got)
	}
}

// TestStatIdentityChangesOnRecreate is the property the whole guard rests on,
// and the one that broke on ext4 with a naive inode-number identity: replacing
// a directory (rmdir+mkdir) must change the identity on ANY filesystem — btrfs
// distinguishes by inode number, ext4 (which recycles the number) by the fresh
// birth time. This runs on whatever filesystem the test host uses, so it pins
// the cross-filesystem robustness directly.
func TestStatIdentityChangesOnRecreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "view")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	before, _ := statIdentity(dir)

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	after, _ := statIdentity(dir)
	if after == before {
		t.Errorf("identity unchanged after rmdir+mkdir (%q) — an orphaned mount would be reused; "+
			"neither inode number nor birth time distinguished the recreate on this filesystem (%s)",
			before, fsType(dir))
	}
}

// fsType names the filesystem behind a path, for the diagnostic when the
// recreate-identity assertion fails (it tells us whether inode reuse or coarse
// btime was the culprit).
func fsType(path string) string {
	out, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return "unknown"
	}
	best := "unknown"
	var bestLen int
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && strings.HasPrefix(path, f[1]) && len(f[1]) >= bestLen {
			best, bestLen = f[2], len(f[1])
		}
	}
	return best
}
