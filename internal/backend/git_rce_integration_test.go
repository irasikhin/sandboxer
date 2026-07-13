//go:build integration

package backend

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/itest"
)

// gitInit makes a throwaway repo with one commit and returns its absolute common
// git dir. git runs on the host (the test machine has it); identity is passed
// per-command so it works regardless of the host's global config.
func gitInit(t *testing.T) (repo, common string) {
	t.Helper()
	repo = t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", repo}, args...)...)
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "f"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "f")
	run("commit", "-qm", "init")
	return repo, run("rev-parse", "--absolute-git-dir")
}

// TestRun_RealEngine_GitDirRCE_ConfigAndHooksReadOnly is the regression guard for
// the git-common-dir RCE: a sandbox must NOT be able to write the repo's `config`
// or `hooks/` (git executes both as commands on the host), yet MUST be able to
// write `objects/` (or it could not commit). Only needs a shell, so it runs on
// the smoke image.
func TestRun_RealEngine_GitDirRCE_ConfigAndHooksReadOnly(t *testing.T) {
	engine := itest.Engine(t)
	image := itest.SmokeImage(t, engine)
	_, common := gitInit(t)

	script := `
c='` + common + `'
if echo x >> "$c/config"        2>/dev/null; then echo CONFIG_WRITABLE; else echo CONFIG_RO; fi
if echo x >  "$c/hooks/evil"    2>/dev/null; then echo HOOKS_WRITABLE;  else echo HOOKS_RO;  fi
if : >       "$c/objects/probe" 2>/dev/null; then echo OBJECTS_WRITABLE; else echo OBJECTS_RO; fi
`
	o := realRunOpts(t, engine, image, t.TempDir(), "sh", "-c", script)
	o.GitCommonDir = common
	var out bytes.Buffer
	o.Stdout, o.Stderr = &out, &out
	if _, err := Run(o); err != nil {
		t.Fatalf("Run: %v\n%s", err, out.String())
	}
	got := out.String()
	for _, want := range []string{"CONFIG_RO", "HOOKS_RO", "OBJECTS_WRITABLE"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in container output:\n%s", want, got)
		}
	}
	// The host repo config/hooks must be untouched.
	if b, _ := os.ReadFile(filepath.Join(common, "config")); strings.Contains(string(b), "\nx") {
		t.Errorf("host repo config was mutated from the sandbox:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(common, "hooks", "evil")); err == nil {
		t.Error("sandbox planted a hook on the host (hooks/evil exists)")
	}
}

// TestRun_RealEngine_GitDir_CommitStillWorks proves the hardening did not break
// the core flow: with config read-only, the agent still commits — objects/refs/
// logs are writable and the identity is injected via GitUserName/GitUserEmail.
// Needs git in the image, so it uses a git-capable image or skips.
func TestRun_RealEngine_GitDir_CommitStillWorks(t *testing.T) {
	engine := itest.Engine(t)
	// The toolbox image ships git and has no entrypoint (so our `sh -c` command
	// runs as given). alpine/git forces `git` as the entrypoint, so it cannot
	// stand in here. Skips locally unless the toolbox image is present.
	image := itest.EnsureToolboxImage(t, engine)
	repo, common := gitInit(t)

	// A worktree on a sandbox branch, as MakeSandbox would create.
	wt := t.TempDir()
	gc := exec.Command("git", "-C", repo, "worktree", "add", "-q", "-b", "sandbox/itest", wt, "HEAD")
	gc.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@e", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@e")
	if out, err := gc.CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}

	o := realRunOpts(t, engine, image, wt,
		"sh", "-c", `cd "$SANDBOXER_SANDBOX_DIR" && echo change >> f && git add f && git commit -qm "from sandbox" && git log --oneline -1 --format='%an <%ae>'`)
	o.GitCommonDir = common
	o.GitUserName, o.GitUserEmail = "Sandbox Dev", "dev@sandbox.local"
	var out bytes.Buffer
	o.Stdout, o.Stderr = &out, &out
	code, err := Run(o)
	if err != nil || code != 0 {
		t.Fatalf("commit run failed (code %d, err %v):\n%s", code, err, out.String())
	}
	if !strings.Contains(out.String(), "dev@sandbox.local") {
		t.Errorf("commit did not use the injected identity:\n%s", out.String())
	}
	// The commit must be visible on the host branch (work comes back as a branch).
	hostLog := exec.Command("git", "-C", repo, "log", "--oneline", "-1", "sandbox/itest", "--format=%s")
	if b, _ := hostLog.CombinedOutput(); !strings.Contains(string(b), "from sandbox") {
		t.Errorf("host does not see the sandbox commit on the branch: %s", b)
	}
}
