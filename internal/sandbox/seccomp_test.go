package sandbox

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestEnsureSeccompProfile: the write lands under _meta with the
// content-addressed name, the pure path twin agrees (show/compose hash the
// same argv enter builds), and a repeat call is a no-op.
func TestEnsureSeccompProfile(t *testing.T) {
	base, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path, err := base.EnsureSeccompProfile()
	if err != nil {
		t.Fatal(err)
	}
	pure, err := base.SeccompProfilePath()
	if err != nil || pure != path {
		t.Errorf("SeccompProfilePath = (%q, %v), want the written path %q", pure, err, path)
	}
	if !strings.Contains(path, "_meta") || !strings.Contains(path, "seccomp-") {
		t.Errorf("profile path %q, want a content-addressed file under _meta", path)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		t.Errorf("written profile = (%v, %v), want a non-empty file", info, err)
	}
	again, err := base.EnsureSeccompProfile()
	if err != nil || again != path {
		t.Errorf("second ensure = (%q, %v), want the same path", again, err)
	}
}

// TestHostSubIDCounts: doctor's view of the host subordinate ranges reads the
// same databases the generation path does, and reports 0 when there are none.
func TestHostSubIDCounts(t *testing.T) {
	dir := t.TempDir()
	uidFile := filepath.Join(dir, "subuid")
	gidFile := filepath.Join(dir, "subgid")
	me := strconv.Itoa(os.Getuid())
	if err := os.WriteFile(uidFile, []byte(me+":100000:65536\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gidFile, []byte(me+":100000:1024\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer func(u, g string) { hostSubuidPath, hostSubgidPath = u, g }(hostSubuidPath, hostSubgidPath)
	hostSubuidPath, hostSubgidPath = uidFile, gidFile
	if u, g := HostSubIDCounts(); u != 65536 || g != 1024 {
		t.Errorf("HostSubIDCounts = (%d, %d), want (65536, 1024)", u, g)
	}
	hostSubuidPath, hostSubgidPath = filepath.Join(dir, "absent"), filepath.Join(dir, "absent")
	if u, g := HostSubIDCounts(); u != 0 || g != 0 {
		t.Errorf("no databases = (%d, %d), want (0, 0)", u, g)
	}
}
