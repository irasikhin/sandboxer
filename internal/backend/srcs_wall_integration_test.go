//go:build integration

package backend

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/itest"
	"github.com/irasikhin/sandboxer/internal/sandbox"
)

// wallRepo builds a repo with an exposed directory, two directories that must
// stay hidden and a root file, and returns its path.
func wallRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	gitHost := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitHost("init", "-q")
	gitHost("config", "user.email", "t@example.com")
	gitHost("config", "user.name", "t")
	gitHost("config", "commit.gpgsign", "false")
	for _, d := range []string{"serviceA", "serviceB", "secrets"} {
		if err := os.MkdirAll(filepath.Join(repo, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, d, "f.txt"), []byte(d), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte("ctx"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitHost("add", "-A")
	gitHost("commit", "-qm", "init")
	return repo
}

// narrowedSandbox creates a sandbox exposing only /serviceA/ and returns the
// base, its slug and the source worktree path.
func narrowedSandbox(t *testing.T, repo string) (*sandbox.Base, string, string) {
	t.Helper()
	b, err := sandbox.ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	slug := itest.Slug("wall")
	if err := b.WriteProfileJSON(slug,
		[]byte(`{"srcs":[{"src":".","branch":"feat/wall","include":["/serviceA/"]}]}`)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox(slug, io.Discard); err != nil {
		t.Fatalf("MakeSandbox: %v", err)
	}
	return b, slug, b.Srcs(slug)[0].Path
}

// runInSandbox runs script in the narrowed sandbox and returns its exit code
// and combined output.
func runInSandbox(t *testing.T, engine, image string, b *sandbox.Base, slug, script string) (int, string) {
	t.Helper()
	mountDest, srcMounts, err := sandbox.Mounts(b.Srcs(slug))
	if err != nil {
		t.Fatal(err)
	}
	if mountDest {
		t.Fatal("a narrowed sandbox asked to mount its root — the wall is gone before the container even starts")
	}
	o := RunOpts{
		Engine: engine, Image: image, Dest: b.SandboxDir(slug), Slug: slug,
		MountDest: mountDest,
		SrcMounts: srcMounts,
		HomeDir:   t.TempDir(),
		NoEgress:  true,
		Args:      []string{"sh", "-c", script},
	}
	var out bytes.Buffer
	o.Stdout, o.Stderr = &out, &out
	code, err := Run(o)
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	return code, out.String()
}

// TestRun_RealEngine_SrcsWall proves the containment model end-to-end on a real
// engine, and it is the test that matters most for view mounts: the excluded
// files are NOT missing from the host — they sit in a complete worktree one
// directory up — so the only thing hiding them is that their directory is never
// mounted. Everything below is checked against a real engine because none of it
// can be proven by inspecting an argv.
func TestRun_RealEngine_SrcsWall(t *testing.T) {
	engine := itest.Engine(t)
	image := itest.SmokeImage(t, engine)
	t.Setenv("SANDBOXER_STATE", t.TempDir())

	repo := wallRepo(t)
	b, slug, wt := narrowedSandbox(t, repo)

	// The host tree is WHOLE — this is the property view mounts exist to give
	// (an IDE can open the branch), and it is what makes the wall load-bearing:
	// everything the container must not see is right there on disk.
	for _, p := range []string{"serviceA", "serviceB", "secrets", "CLAUDE.md"} {
		if _, err := os.Stat(filepath.Join(wt, p)); err != nil {
			t.Fatalf("host worktree is not whole — %s missing: %v", p, err)
		}
	}

	script := fmt.Sprintf(`set -e
# what is exposed is there, and readable
test -f %[1]s/serviceA/f.txt || exit 10
grep -q serviceA %[1]s/serviceA/f.txt || exit 11
# what is NOT exposed does not exist — not merely unreadable
test ! -e %[1]s/serviceB || exit 12
test ! -e %[1]s/secrets || exit 13
test ! -e %[1]s/CLAUDE.md || exit 14
# and it cannot be reached by walking up to the unmounted root either
ls %[1]s/.. >/dev/null 2>&1 && ls %[1]s/../.. >/dev/null 2>&1
find / -name 'CLAUDE.md' 2>/dev/null | grep -q . && exit 15
find / -path '*/secrets/f.txt' 2>/dev/null | grep -q . && exit 16
# git cannot work: its metadata is never mounted
if command -v git >/dev/null 2>&1; then
  git -C %[1]s/serviceA status >/dev/null 2>&1 && exit 17
fi
# a NEW file in the exposed directory is real work on the host
printf agent > %[1]s/serviceA/new.txt
mkdir -p %[1]s/serviceA/newdir && printf nested > %[1]s/serviceA/newdir/deep.txt
echo walled`, wt)

	code, out := runInSandbox(t, engine, image, b, slug, script)
	if code != 0 {
		t.Fatalf(`exit = %d, want 0
 10=exposed file missing 11=unreadable 12/13/14=WALL BREACHED (excluded path visible)
 15/16=WALL BREACHED (excluded content reachable elsewhere) 17=git worked inside
%s`, code, out)
	}
	if !strings.Contains(out, "walled") {
		t.Fatalf("stdout = %q, want 'walled'", out)
	}

	// New files land in the host worktree, where plain host git sees them.
	for name, want := range map[string]string{
		"serviceA/new.txt":         "agent",
		"serviceA/newdir/deep.txt": "nested",
	} {
		if data, err := os.ReadFile(filepath.Join(wt, name)); err != nil || string(data) != want {
			t.Errorf("new file %s did not reach the host worktree: %q (err %v)", name, data, err)
		}
	}
	st, err := exec.Command("git", "-C", wt, "status", "--porcelain").Output()
	if err != nil {
		t.Fatalf("host git status in worktree: %v", err)
	}
	for _, want := range []string{"serviceA/new.txt", "serviceA/newdir/"} {
		if !strings.Contains(string(st), want) {
			t.Errorf("host git does not see %q:\n%s", want, st)
		}
	}
	// The host tree is still whole, and the container never touched what it
	// could not see.
	if data, err := os.ReadFile(filepath.Join(wt, "secrets", "f.txt")); err != nil || string(data) != "secrets" {
		t.Errorf("excluded file changed under the container: %q (err %v)", data, err)
	}
}

// TestRun_RealEngine_WriteOutsideViewFails: a write to a path the sandbox does
// not expose must FAIL rather than land in the container's ephemeral layer and
// disappear with it. Nothing in sandboxer arranges this — the engine
// materializes the unmounted parents as root-owned directories and the sandbox
// runs as the host uid (see containerUserArgs), so the kernel refuses the write.
// The test pins that behavior because the alternative is silent data loss.
func TestRun_RealEngine_WriteOutsideViewFails(t *testing.T) {
	engine := itest.Engine(t)
	image := itest.SmokeImage(t, engine)
	t.Setenv("SANDBOXER_STATE", t.TempDir())

	repo := wallRepo(t)
	b, slug, wt := narrowedSandbox(t, repo)

	script := fmt.Sprintf(`
# outside the exposed directory: every write is refused
touch %[1]s/ghost.txt 2>/dev/null && exit 20
mkdir -p %[1]s/ghostdir 2>/dev/null && exit 21
touch %[1]s/../ghost.txt 2>/dev/null && exit 22
# inside it: writes work
touch %[1]s/serviceA/real.txt || exit 23
echo denied`, wt)

	code, out := runInSandbox(t, engine, image, b, slug, script)
	if code != 0 {
		t.Fatalf(`exit = %d, want 0
 20/21/22=a write outside the view SUCCEEDED (it would vanish with the container)
 23=a write inside the view was refused
%s`, code, out)
	}
	if !strings.Contains(out, "denied") {
		t.Fatalf("stdout = %q, want 'denied'", out)
	}
	if _, err := os.Stat(filepath.Join(wt, "serviceA", "real.txt")); err != nil {
		t.Errorf("in-view write did not reach the host: %v", err)
	}
	for _, ghost := range []string{"ghost.txt", "ghostdir"} {
		if _, err := os.Stat(filepath.Join(wt, ghost)); !os.IsNotExist(err) {
			t.Errorf("%s exists on the host (err=%v)", ghost, err)
		}
	}
}

// TestRun_RealEngine_UnnarrowedMountsWholeTree is the other half of the switch:
// with no include, the sandbox root is mounted and the container sees the whole
// repository — the behavior every unnarrowed sandbox has always had.
func TestRun_RealEngine_UnnarrowedMountsWholeTree(t *testing.T) {
	engine := itest.Engine(t)
	image := itest.SmokeImage(t, engine)
	t.Setenv("SANDBOXER_STATE", t.TempDir())

	repo := wallRepo(t)
	b, err := sandbox.ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	slug := itest.Slug("whole")
	if err := b.WriteProfileJSON(slug, []byte(`{"srcs":[{"src":".","branch":"feat/whole"}]}`)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox(slug, io.Discard); err != nil {
		t.Fatalf("MakeSandbox: %v", err)
	}
	wt := b.Srcs(slug)[0].Path

	mountDest, srcMounts, err := sandbox.Mounts(b.Srcs(slug))
	if err != nil {
		t.Fatal(err)
	}
	if !mountDest {
		t.Fatal("an unnarrowed sandbox does not mount its root — it would see nothing")
	}
	script := fmt.Sprintf(`set -e
test -f %[1]s/serviceA/f.txt || exit 10
test -f %[1]s/serviceB/f.txt || exit 11
test -f %[1]s/CLAUDE.md || exit 12
echo whole`, wt)

	o := RunOpts{
		Engine: engine, Image: image, Dest: b.SandboxDir(slug), Slug: slug,
		MountDest: mountDest, SrcMounts: srcMounts,
		HomeDir: t.TempDir(), NoEgress: true,
		Args: []string{"sh", "-c", script},
	}
	var out bytes.Buffer
	o.Stdout, o.Stderr = &out, &out
	code, err := Run(o)
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (10/11/12 = a file of the whole repo is missing)\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "whole") {
		t.Errorf("stdout = %q, want 'whole'", out.String())
	}
}

// TestRun_RealEngine_OrphanedMountIsCaught demonstrates, on a real engine, the
// concrete hazard the mount fingerprint (RunOpts.MountGen) exists to catch: a
// bind mount is pinned to the inode it named at mount time, so when the host
// removes and recreates that directory (a git checkout switching branches), a
// LIVE container keeps seeing the orphaned old inode — stale reads, writes into
// a directory nobody looks at. The test proves both halves: the engine really
// does orphan the mount, and sandbox.MountFingerprint changes across the
// recreate, which is what flips the session hash so the next enter rebuilds.
func TestRun_RealEngine_OrphanedMountIsCaught(t *testing.T) {
	engine := itest.Engine(t)
	image := itest.SmokeImage(t, engine)

	host := t.TempDir()
	view := filepath.Join(host, "view")
	if err := os.MkdirAll(view, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(view, "marker"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}

	// fingerprint BEFORE — this is what a session would carry in its hash.
	before := sandbox.MountFingerprint([]string{view})
	if before == "" {
		t.Fatal("empty fingerprint for a real dir")
	}

	// A detached container holding the bind mount open — the live session.
	name := itest.Slug("orphan")
	runOut, err := exec.Command(engine, "run", "-d", "--name", name,
		"-v", view+":"+view, image, "sleep", "infinity").CombinedOutput()
	if err != nil {
		t.Fatalf("run -d: %v\n%s", err, runOut)
	}
	defer exec.Command(engine, "rm", "-f", name).Run()

	// On the host: recreate the mounted directory, as a checkout would (rmdir +
	// mkdir → new inode), with new content.
	if err := os.RemoveAll(view); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(view, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(view, "marker"), []byte("updated"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The live container is now bound to the orphaned inode, so it does NOT see
	// the host's fresh content — it reads the old inode (whose marker the host's
	// rm already unlinked, so "GONE") rather than "updated". Either way it has
	// diverged from the host, which is the hazard.
	seen, err := exec.Command(engine, "exec", name, "sh", "-c",
		"cat "+filepath.Join(view, "marker")+" 2>/dev/null || echo GONE").CombinedOutput()
	if err != nil {
		t.Fatalf("exec: %v\n%s", err, seen)
	}
	if got := strings.TrimSpace(string(seen)); got == "updated" {
		t.Fatalf("container saw the host's fresh content (%q) — no orphaning occurred, "+
			"which would mean this guard is unnecessary; investigate before trusting the test", got)
	} else {
		t.Logf("container is orphaned from the host as expected (sees %q, host has \"updated\")", got)
	}

	// The guard: the fingerprint changed, so the session hash would flip and the
	// next enter rebuilds against the fresh directory instead of the orphan.
	after := sandbox.MountFingerprint([]string{view})
	if after == before {
		t.Fatalf("MountFingerprint did not change across the host recreate (%q) — the orphaned mount would be reused", before)
	}
}

// TestRun_RealEngine_NestedInclude pins the nested-include behavior on a real
// engine: listing a directory AND a child of it (redundant, since the parent
// already exposes the child) produces two OVERLAPPING bind mounts. The engine
// must accept them, the parent must expose everything under it (the child entry
// adds nothing), and a sibling directory OUTSIDE the includes must stay hidden —
// the redundancy must not widen the wall. sandbox.Mounts sorts parent before
// child so the nested binds apply in the order the engine needs.
func TestRun_RealEngine_NestedInclude(t *testing.T) {
	engine := itest.Engine(t)
	image := itest.SmokeImage(t, engine)
	t.Setenv("SANDBOXER_STATE", t.TempDir())

	// A repo where the exposed dir has children, plus a sibling to stay hidden.
	repo := t.TempDir()
	gitHost := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitHost("init", "-q")
	gitHost("config", "user.email", "t@example.com")
	gitHost("config", "user.name", "t")
	gitHost("config", "commit.gpgsign", "false")
	for _, f := range []string{"src/proto/a.txt", "src/other/b.txt", "secret/s.txt"} {
		p := filepath.Join(repo, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitHost("add", "-A")
	gitHost("commit", "-qm", "init")

	b, err := sandbox.ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	slug := itest.Slug("nested")
	// The child is listed FIRST, to prove the sorted mount order (parent first)
	// is what actually reaches the engine.
	if err := b.WriteProfileJSON(slug,
		[]byte(`{"srcs":[{"src":".","branch":"feat/nested","include":["/src/proto/","/src/"]}]}`)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox(slug, io.Discard); err != nil {
		t.Fatalf("MakeSandbox: %v", err)
	}
	wt := b.Srcs(slug)[0].Path

	mountDest, srcMounts, err := sandbox.Mounts(b.Srcs(slug))
	if err != nil {
		t.Fatal(err)
	}
	if mountDest {
		t.Fatal("nested-include sandbox asked to mount its root")
	}
	if len(srcMounts) != 2 {
		t.Fatalf("mounts = %v, want the parent and the child", srcMounts)
	}

	script := fmt.Sprintf(`set -e
# the parent exposes everything under it — the child entry adds nothing
test -f %[1]s/src/proto/a.txt || exit 10
test -f %[1]s/src/other/b.txt || exit 11
# the sibling outside the includes stays hidden — redundancy must not widen the wall
test ! -e %[1]s/secret || exit 12
echo nested-ok`, wt)

	o := RunOpts{
		Engine: engine, Image: image, Dest: b.SandboxDir(slug), Slug: slug,
		MountDest: mountDest, SrcMounts: srcMounts,
		HomeDir: t.TempDir(), NoEgress: true,
		Args: []string{"sh", "-c", script},
	}
	var out bytes.Buffer
	o.Stdout, o.Stderr = &out, &out
	code, err := Run(o)
	if err != nil {
		t.Fatalf("Run (engine rejected overlapping mounts?): %v\n%s", err, out.String())
	}
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (10/11 = parent did not expose a child, 12 = WALL BREACHED)\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "nested-ok") {
		t.Errorf("stdout = %q, want 'nested-ok'", out.String())
	}
}
