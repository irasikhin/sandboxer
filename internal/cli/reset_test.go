package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitIn runs git in dir and returns trimmed combined output, failing the test
// on error — the reset tests need to stage commits and inspect refs directly.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commitFile writes name=content in dir and commits it, returning the new HEAD.
func commitFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-qm", "c-"+name)
	return gitIn(t, dir, "rev-parse", "HEAD")
}

// worktreeOf returns the single source worktree path of the "feat" sandbox.
func worktreeOf(t *testing.T, project string) string {
	t.Helper()
	lines := pathLines(t, "feat", "--src", project)
	if len(lines) != 1 {
		t.Fatalf("path feat = %v, want one worktree", lines)
	}
	return lines[0]
}

// TestResetOntoRef: reset --onto <ref> moves the source's branch onto that ref
// while staying ON the branch (no checkout), leaving the worktree at the ref.
func TestResetOntoRef(t *testing.T) {
	project := newProject(t)
	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}
	wt := worktreeOf(t, project)

	// Advance the project's default branch past where feat/feat was cut.
	target := commitFile(t, project, "base.txt", "merged\n")

	if code, _, errs := run("reset", "feat", "--onto", target, "--no-fetch", "--src", project); code != 0 {
		t.Fatalf("reset: %d %s", code, errs)
	}
	if got := gitIn(t, wt, "rev-parse", "HEAD"); got != target {
		t.Errorf("worktree HEAD = %q, want the base %q", got, target)
	}
	if br := gitIn(t, wt, "rev-parse", "--abbrev-ref", "HEAD"); br != "feat/feat" {
		t.Errorf("worktree branch = %q, want it to stay on feat/feat", br)
	}
}

// TestResetRefusesDirty: a source with uncommitted changes is refused, then
// --force discards them and resets.
func TestResetRefusesDirty(t *testing.T) {
	project := newProject(t)
	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}
	wt := worktreeOf(t, project)
	if err := os.WriteFile(filepath.Join(wt, "f.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, errs := run("reset", "feat", "--onto", "HEAD", "--no-fetch", "--src", project)
	if code == 0 || !strings.Contains(errs, "uncommitted") {
		t.Fatalf("dirty reset = %d %q, want refusal naming uncommitted changes", code, errs)
	}

	if code, _, errs := run("reset", "feat", "--onto", "HEAD", "--no-fetch", "--force", "--src", project); code != 0 {
		t.Fatalf("reset --force: %d %s", code, errs)
	}
	if st := gitIn(t, wt, "status", "--porcelain"); st != "" {
		t.Errorf("worktree still dirty after --force: %q", st)
	}
}

// TestResetUnknownSource: naming a source that does not exist fails and lists
// the real sources (shared FindSource path).
func TestResetUnknownSource(t *testing.T) {
	project := newProject(t)
	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}
	code, _, errs := run("reset", "feat", "nope", "--no-fetch", "--src", project)
	if code == 0 || !strings.Contains(errs, "no source") {
		t.Errorf("reset feat nope = %d %q, want unknown-source rejection", code, errs)
	}
}

// TestResetBadBase: an unresolvable base ref fails in the pre-flight, before
// any branch is moved.
func TestResetBadBase(t *testing.T) {
	project := newProject(t)
	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}
	wt := worktreeOf(t, project)
	before := gitIn(t, wt, "rev-parse", "HEAD")

	code, _, errs := run("reset", "feat", "--onto", "no/such/ref", "--no-fetch", "--src", project)
	if code == 0 || !strings.Contains(errs, "cannot resolve base") {
		t.Fatalf("bad base = %d %q, want a resolve error", code, errs)
	}
	if after := gitIn(t, wt, "rev-parse", "HEAD"); after != before {
		t.Errorf("worktree moved on a failed reset: %q -> %q", before, after)
	}
}

// TestResetSkipsAdopted: an adopted source (its branch is the repo's own
// checkout) is not reset — sandboxer does not own it.
func TestResetSkipsAdopted(t *testing.T) {
	project := newProject(t)
	def := gitIn(t, project, "rev-parse", "--abbrev-ref", "HEAD") // the checked-out branch
	cfg := filepath.Join(t.TempDir(), "ad.nix")
	body := "{ name = \"ad\"; srcs = [ { src = \".\"; branch = \"" + def + "\"; } ]; }\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("create", "--src", project, "--config", cfg); code != 0 {
		t.Fatalf("create adopted: %d %s", code, errs)
	}

	code, out, errs := run("reset", "ad", "--no-fetch", "--src", project)
	if code != 0 {
		t.Fatalf("reset adopted: %d %s", code, errs)
	}
	if !strings.Contains(errs, "adopted") || !strings.Contains(errs, "nothing to reset") {
		t.Errorf("reset adopted stderr = %q, want it to skip and say nothing reset", errs)
	}
	if strings.Contains(out, "reset:") {
		t.Errorf("reset adopted printed a reset line: %q", out)
	}
}

// TestResetFetchesDefaultBase: with no --onto, reset fetches and re-bases onto
// the fetched origin/main — the default post-merge path end to end.
func TestResetFetchesDefaultBase(t *testing.T) {
	project := newProject(t)
	bare := t.TempDir()
	gitIn(t, bare, "init", "--bare", "-q")
	gitIn(t, project, "remote", "add", "origin", bare)
	gitIn(t, project, "push", "-q", "origin", "HEAD:main") // origin/main = C0

	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}
	wt := worktreeOf(t, project)

	// Advance origin/main to C1 through a separate clone (project's origin/main
	// stays stale until reset fetches).
	clone := t.TempDir()
	gitIn(t, clone, "clone", "-q", "-b", "main", bare, ".")
	gitIn(t, clone, "config", "user.email", "t@example.com")
	gitIn(t, clone, "config", "user.name", "t")
	gitIn(t, clone, "config", "commit.gpgsign", "false")
	target := commitFile(t, clone, "merged.txt", "on main\n")
	gitIn(t, clone, "push", "-q", "origin", "HEAD:main")

	if code, _, errs := run("reset", "feat", "--src", project); code != 0 {
		t.Fatalf("reset: %d %s", code, errs)
	}
	if got := gitIn(t, wt, "rev-parse", "HEAD"); got != target {
		t.Errorf("worktree HEAD = %q, want the fetched origin/main %q", got, target)
	}
}
