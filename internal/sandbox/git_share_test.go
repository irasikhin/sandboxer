package sandbox

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
)

// TestSrcsRecordGitDirAndMode resolves a source that opted into git = "ro" and
// pins the invariant the whole share rests on: the recorded GitDir is the
// PREFIX of the path the worktree's .git pointer file names, so mounting that
// one directory at its own host path is exactly what makes the pointer resolve
// inside the guest — no rewriting of a file the host also reads.
func TestSrcsRecordGitDirAndMode(t *testing.T) {
	repo := gitRepoWithCommit(t)
	b, err := ResolveBase(repo)
	if err != nil {
		t.Fatalf("ResolveBase: %v", err)
	}
	if err := b.WriteProfileJSON("g", []byte(`{"srcs":[{"src":".","branch":"feat/g","git":"ro"}]}`)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("g", io.Discard); err != nil {
		t.Fatalf("MakeSandbox: %v", err)
	}
	srcs := b.Srcs("g")
	if len(srcs) != 1 {
		t.Fatalf("srcs = %+v, want one source", srcs)
	}
	s := srcs[0]
	if s.Git != config.GitRO {
		t.Errorf("Git = %q, want %q", s.Git, config.GitRO)
	}
	if s.GitDir == "" {
		t.Fatal("GitDir was not recorded — the mount assembly would have nothing to share")
	}

	pointer, err := os.ReadFile(filepath.Join(s.Path, ".git"))
	if err != nil {
		t.Fatalf("the worktree's .git pointer file: %v", err)
	}
	target := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(pointer)), "gitdir:"))
	if !strings.HasPrefix(target, s.GitDir) {
		t.Errorf("the worktree points at %q, which is not under the shared git dir %q — "+
			"sharing it would not make git work inside", target, s.GitDir)
	}

	mounts := GitMounts(srcs)
	if len(mounts) != 1 || mounts[0].Source != s.GitDir || mounts[0].Mode != config.GitRO {
		t.Fatalf("GitMounts = %+v, want the recorded git dir shared read-only", mounts)
	}
}

// TestSrcsGitDirOfALinkedWorktree: a source may itself be a linked worktree of
// some other repo (its .git is a pointer file too). What must be shared then is
// the ORIGINAL repository's common git dir — the objects and refs live there,
// not in the worktree's admin subdir — which is why the resolution asks git for
// the common dir instead of assuming <src>/.git.
func TestSrcsGitDirOfALinkedWorktree(t *testing.T) {
	origin := gitRepoWithCommit(t)
	linked := filepath.Join(t.TempDir(), "linked")
	runGit(t, origin, "worktree", "add", "-q", "-b", "side", linked)

	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatalf("ResolveBase: %v", err)
	}
	profile := `{"srcs":[{"src":` + quote(linked) + `,"branch":"feat/g","git":"rw"}]}`
	if err := b.WriteProfileJSON("g", []byte(profile)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("g", io.Discard); err != nil {
		t.Fatalf("MakeSandbox: %v", err)
	}
	srcs := b.Srcs("g")
	if len(srcs) != 1 {
		t.Fatalf("srcs = %+v, want one source", srcs)
	}
	want, err := filepath.EvalSymlinks(filepath.Join(origin, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := filepath.EvalSymlinks(srcs[0].GitDir)
	if err != nil {
		t.Fatalf("recorded GitDir %q: %v", srcs[0].GitDir, err)
	}
	if got != want {
		t.Errorf("GitDir = %q, want the origin repository's common git dir %q", got, want)
	}
}

// TestSrcsGitDirOfARemoteSource: a remote src is worktree'd from the host-side
// cache clone, so the git dir a share exposes is the CACHE's — never anything
// of the user's. The remote branch of the resolve has its own git-dir lookup,
// which this pins.
func TestSrcsGitDirOfARemoteSource(t *testing.T) {
	origin := gitRepoWithCommit(t)
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	profile := `{"srcs":[{"src":"file://` + origin + `","branch":"feat/rem","git":"ro"}]}`
	if err := b.WriteProfileJSON("rem", []byte(profile)); err != nil {
		t.Fatal(err)
	}
	if err := b.MakeSandbox("rem", io.Discard); err != nil {
		t.Fatalf("MakeSandbox: %v", err)
	}
	srcs := b.Srcs("rem")
	if len(srcs) != 1 {
		t.Fatalf("srcs = %+v, want one source", srcs)
	}
	if !strings.HasPrefix(srcs[0].GitDir, b.remotesDir()) {
		t.Errorf("GitDir = %q, want it inside the remotes cache %q", srcs[0].GitDir, b.remotesDir())
	}
	if got := GitMounts(srcs); len(got) != 1 || got[0].Source != srcs[0].GitDir {
		t.Errorf("GitMounts = %+v, want the cache clone's git dir", got)
	}
}

// quote renders a path as a JSON string for the inline profile fixtures.
func quote(s string) string { return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"` }
