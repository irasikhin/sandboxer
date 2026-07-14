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

// TestRun_RealEngine_SrcsWall proves the srcs containment model end-to-end on
// a real engine: the container sees ONLY what the include patterns selected
// (non-cone sparse worktree contents), git does not function inside (no git
// metadata is mounted), and a write from the container lands in the host
// worktree where plain host git picks it up.
func TestRun_RealEngine_SrcsWall(t *testing.T) {
	engine := itest.Engine(t)
	image := itest.SmokeImage(t, engine)
	t.Setenv("SANDBOXER_STATE", t.TempDir())

	repo := t.TempDir()
	gitHost := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitHost("init", "-q")
	gitHost("config", "user.email", "t@example.com")
	gitHost("config", "user.name", "t")
	for _, d := range []string{"serviceA", "serviceB"} {
		if err := os.MkdirAll(filepath.Join(repo, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, d, "f.txt"), []byte(d), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitHost("add", "-A")
	gitHost("commit", "-qm", "init")

	b, err := sandbox.ResolveBase(repo)
	if err != nil {
		t.Fatal(err)
	}
	slug := itest.Slug("wall")
	if err := b.WriteProfileJSON(slug, []byte(`{"srcs":[{"src":".","include":["/serviceA/"]}]}`)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox(slug, io.Discard); err != nil {
		t.Fatalf("MakeSandbox: %v", err)
	}
	srcs := b.Srcs(slug)
	wt := srcs[0].Path

	// Inside the container: the selection is there, everything else is not,
	// and git — when the image even has it — cannot resolve the repo (its
	// metadata lives only on the host).
	script := fmt.Sprintf(`set -e
test -f %[1]s/serviceA/f.txt || exit 10
test ! -e %[1]s/serviceB || exit 11
if command -v git >/dev/null 2>&1; then
  git -C %[1]s status >/dev/null 2>&1 && exit 13
fi
printf agent > %[1]s/serviceA/out.txt
echo walled`, wt)

	o := RunOpts{
		Engine: engine, Image: image, Dest: b.SandboxDir(slug), Slug: slug,
		SrcMounts: sandbox.SrcMounts(srcs), // none here — the managed tree rides the Dest mount
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
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (10=selection missing 11=wall breached 13=git worked)\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "walled") {
		t.Errorf("stdout = %q, want 'walled'", out.String())
	}

	// On the host: the container's write is in the worktree, visible to git.
	if data, err := os.ReadFile(filepath.Join(wt, "serviceA", "out.txt")); err != nil || string(data) != "agent" {
		t.Errorf("container write did not land in the host worktree: %q (err %v)", data, err)
	}
	st, err := exec.Command("git", "-C", wt, "status", "--porcelain").Output()
	if err != nil {
		t.Fatalf("host git status in worktree: %v", err)
	}
	if !strings.Contains(string(st), "serviceA/out.txt") {
		t.Errorf("host git does not see the container's write:\n%s", st)
	}
}
