package sandbox

import (
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
)

// TestGitMountsOffByDefault pins the default posture: with no git key on any
// source, nothing about the repository is shared — the whole "git never enters
// the sandbox" property is this one empty list.
func TestGitMountsOffByDefault(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  Source
	}{
		{name: "absent", src: Source{Path: "/wt", GitDir: "/repo/.git"}},
		{name: "explicit off", src: Source{Path: "/wt", GitDir: "/repo/.git", Git: config.GitOff}},
		{name: "shared but unresolved git dir", src: Source{Path: "/wt", Git: config.GitRO}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := GitMounts([]Source{tc.src}); len(got) != 0 {
				t.Errorf("GitMounts = %v, want none", got)
			}
		})
	}
}

// TestGitMountsShared checks the shape of a shared git dir: identity-mapped
// (source == target, which is what makes the worktree's .git pointer resolve
// inside the guest) and carrying the mode verbatim.
func TestGitMountsShared(t *testing.T) {
	for _, mode := range []string{config.GitRO, config.GitRW} {
		t.Run(mode, func(t *testing.T) {
			got := GitMounts([]Source{{Path: "/wt", GitDir: "/repo/.git", Git: mode}})
			if len(got) != 1 {
				t.Fatalf("GitMounts = %v, want one mount", got)
			}
			if got[0].Source != "/repo/.git" || got[0].Target != "/repo/.git" {
				t.Errorf("GitMounts[0] = %+v, want the git dir mapped onto its own host path", got[0])
			}
			if got[0].Mode != mode {
				t.Errorf("mode = %q, want %q", got[0].Mode, mode)
			}
		})
	}
}

// TestGitMountsSortedAndDeduped pins the argv contract: the shares land in the
// create argv, so their order must come from the paths and never from the srcs
// order — otherwise a config reshuffle would flip the session hash and rebuild
// a machine that runs with exactly the same shares.
func TestGitMountsSortedAndDeduped(t *testing.T) {
	got := GitMounts([]Source{
		{Path: "/wt/b", GitDir: "/repo/b/.git", Git: config.GitRO},
		{Path: "/wt/a", GitDir: "/repo/a/.git", Git: config.GitRW},
		{Path: "/wt/a2", GitDir: "/repo/a/.git", Git: config.GitRW}, // same repo twice
	})
	if len(got) != 2 {
		t.Fatalf("GitMounts = %v, want two mounts (the repeated git dir collapses)", got)
	}
	if got[0].Source != "/repo/a/.git" || got[1].Source != "/repo/b/.git" {
		t.Errorf("GitMounts = %v, want them sorted by path", got)
	}
}

// TestGitMountsOnlyTheOptedInSources keeps the key per-source: one source
// asking for git must not hand the sandbox another repository's history.
func TestGitMountsOnlyTheOptedInSources(t *testing.T) {
	got := GitMounts([]Source{
		{Path: "/wt/app", GitDir: "/repo/app/.git", Git: config.GitRO},
		{Path: "/wt/secrets", GitDir: "/repo/secrets/.git"},
	})
	if len(got) != 1 || got[0].Source != "/repo/app/.git" {
		t.Fatalf("GitMounts = %v, want only the source that opted in", got)
	}
}
