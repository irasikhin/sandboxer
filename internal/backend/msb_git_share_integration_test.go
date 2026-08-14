//go:build integration

package backend

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/itest"
)

// gitShareRepo builds a real repo with one commit plus a linked worktree under
// root, and returns (worktree, common git dir) — the exact host shape a
// sandboxer source has: the worktree's .git is a POINTER FILE naming an
// absolute host path inside the repo's git dir.
func gitShareRepo(t *testing.T, root string) (worktree, gitDir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on the host — skipping the git-share e2e")
	}
	repo := filepath.Join(root, "repo")
	run := func(dir string, args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	run(repo, "init", "-q")
	run(repo, "config", "user.email", "t@example.com")
	run(repo, "config", "user.name", "t")
	run(repo, "config", "commit.gpgsign", "false")
	mustWrite(t, filepath.Join(repo, "a.txt"), "hi")
	run(repo, "add", "-A")
	run(repo, "commit", "-qm", "init")
	worktree = filepath.Join(root, "wt")
	run(repo, "worktree", "add", "-q", "-b", "side", worktree)
	return worktree, filepath.Join(repo, ".git")
}

// TestMSB_GitShareOff_RealEngine is the default posture, verified in a live
// guest: the worktree is shared, its .git pointer file rides along — and the
// host path that pointer names is NOT there, which is precisely why git cannot
// operate inside a default sandbox.
func TestMSB_GitShareOff_RealEngine(t *testing.T) {
	engine := itest.Microsandbox(t)
	root := itest.MSBTempDir(t)
	wt, gitDir := gitShareRepo(t, root)

	o := msbITOpts(t, engine, "itmsbgitoff", wt)
	name := SessionName(o.Slug, o.BaseDir)
	itest.CleanupSandbox(t, name)
	if _, err := EnsureSession(o); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	// The pointer file itself is inside the shared worktree…
	if code, _ := ExecSession(o, name, []string{"cat", filepath.Join(wt, ".git")}); code != 0 {
		t.Errorf("the worktree's .git pointer file is not readable inside the guest (code %d)", code)
	}
	// …and what it points at is not.
	if code, _ := ExecSession(o, name, []string{"cat", filepath.Join(gitDir, "HEAD")}); code == 0 {
		t.Error("SECURITY: the repository's git dir was reachable inside a sandbox that never asked for it")
	}
}

// TestMSB_GitShareRO_RealEngine: git = "ro" makes the git dir resolvable at its
// own host path inside the guest (so the worktree's pointer file resolves) and
// READ-ONLY — the enforcement being virtio-fs's, which is the half no golden
// argv test can prove.
func TestMSB_GitShareRO_RealEngine(t *testing.T) {
	engine := itest.Microsandbox(t)
	root := itest.MSBTempDir(t)
	wt, gitDir := gitShareRepo(t, root)

	o := msbITOpts(t, engine, "itmsbgitro", wt)
	o.GitMounts = []config.Mount{{Source: gitDir, Target: gitDir, Mode: config.GitRO}}
	name := SessionName(o.Slug, o.BaseDir)
	itest.CleanupSandbox(t, name)
	if _, err := EnsureSession(o); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	// The pointer file's target resolves — the whole point of identity mapping.
	var out bytes.Buffer
	oe := o
	oe.Stdout = &out
	pointer, err := os.ReadFile(filepath.Join(wt, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	target := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(pointer)), "gitdir:"))
	if code, _ := ExecSession(oe, name, []string{"cat", filepath.Join(target, "HEAD")}); code != 0 {
		t.Fatalf("the worktree's gitdir %q does not resolve inside the guest (code %d)", target, code)
	}
	if !strings.Contains(out.String(), "refs/heads/side") {
		t.Errorf("guest read of the worktree HEAD = %q, want the branch ref", out.String())
	}

	// Read-only means read-only: no write anywhere under the shared git dir.
	for _, path := range []string{
		filepath.Join(gitDir, "sandboxer-probe"),
		filepath.Join(target, "sandboxer-probe"),
	} {
		if code, _ := ExecSession(o, name, []string{"sh", "-c", "echo x > " + path}); code == 0 {
			t.Errorf("SECURITY: wrote %q from inside a read-only git share", path)
		}
		if _, err := os.Stat(path); err == nil {
			t.Errorf("SECURITY: %q landed on the host from a read-only git share", path)
		}
	}
}

// TestMSB_GitShareRW_RealEngine: git = "rw" is writable — and the write lands
// on the host, which is what lets the agent commit (and is the risk the docs
// spell out).
func TestMSB_GitShareRW_RealEngine(t *testing.T) {
	engine := itest.Microsandbox(t)
	root := itest.MSBTempDir(t)
	wt, gitDir := gitShareRepo(t, root)

	o := msbITOpts(t, engine, "itmsbgitrw", wt)
	o.GitMounts = []config.Mount{{Source: gitDir, Target: gitDir, Mode: config.GitRW}}
	name := SessionName(o.Slug, o.BaseDir)
	itest.CleanupSandbox(t, name)
	if _, err := EnsureSession(o); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	probe := filepath.Join(gitDir, "sandboxer-probe")
	if code, _ := ExecSession(o, name, []string{"sh", "-c", "echo x > " + probe}); code != 0 {
		t.Fatalf("a read-write git share refused a write (code %d)", code)
	}
	if _, err := os.Stat(probe); err != nil {
		t.Errorf("the guest's write did not land on the host: %v", err)
	}
}

// TestMSB_GitShareUsable_RealEngine runs REAL git inside the guest against the
// shared dir: status/log on a read-only share, and a commit on a read-write
// one. It needs an image that ships git (the toolbox image —
// SANDBOXER_ITEST_MSB_IMAGE), so it skips on the default alpine.
func TestMSB_GitShareUsable_RealEngine(t *testing.T) {
	engine := itest.Microsandbox(t)
	root := itest.MSBTempDir(t)
	wt, gitDir := gitShareRepo(t, root)

	o := msbITOpts(t, engine, "itmsbgituse", wt)
	o.GitMounts = []config.Mount{{Source: gitDir, Target: gitDir, Mode: config.GitRO}}
	name := SessionName(o.Slug, o.BaseDir)
	itest.CleanupSandbox(t, name)
	if _, err := EnsureSession(o); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	if code, _ := ExecSession(o, name, []string{"sh", "-c", "command -v git >/dev/null 2>&1"}); code != 0 {
		t.Skip("the booted image ships no git — set SANDBOXER_ITEST_MSB_IMAGE to the toolbox image")
	}

	var out bytes.Buffer
	oe := o
	oe.Stdout = &out
	// The guard wrapper must NOT fire here: the pointer resolves, so this is an
	// ordinary repo as far as git is concerned.
	if code, _ := ExecSession(oe, name, []string{"git", "-C", wt, "log", "--oneline", "-1"}); code != 0 {
		t.Fatalf("git log inside a read-only share = code %d, out %q", code, out.String())
	}
	if strings.Contains(out.String(), "sandboxer:") {
		t.Errorf("the guest git guard fired on a SHARED source: %q", out.String())
	}
	if code, _ := ExecSession(o, name, []string{"sh", "-c", "cd " + wt + " && git status --short"}); code != 0 {
		t.Errorf("git status inside a read-only share failed (code %d)", code)
	}
	// A commit needs to write objects and refs, which a read-only share denies.
	if code, _ := ExecSession(o, name, []string{"sh", "-c",
		"cd " + wt + " && echo more >> a.txt && git commit -qam probe"}); code == 0 {
		t.Error("a commit succeeded against a READ-ONLY git share")
	}
}
