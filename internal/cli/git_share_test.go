package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/sandbox"
)

// gitShareTarget records a sandbox whose single source opted into a git share,
// and returns a target over it.
func gitShareTarget(t *testing.T, mode string) *target {
	t.Helper()
	base, err := sandbox.ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srcs := []sandbox.Source{{
		RepoRoot: "/repo", Path: "/wt/repo", Branch: "feat/x", Managed: true,
		Git: mode, GitDir: "/repo/.git",
	}}
	data, err := json.Marshal(srcs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base.SrcsMetaPath("s"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return &target{base: base, slug: "s"}
}

// TestMountPlanCarriesGitShares: the plan every launching command builds picks
// up the recorded share, and leaves it out for a default source.
func TestMountPlanCarriesGitShares(t *testing.T) {
	mp, err := gitShareTarget(t, config.GitRO).mounts()
	if err != nil {
		t.Fatal(err)
	}
	if len(mp.Git) != 1 || mp.Git[0].Source != "/repo/.git" || mp.Git[0].Mode != config.GitRO {
		t.Fatalf("plan git shares = %+v, want the recorded git dir read-only", mp.Git)
	}

	off, err := gitShareTarget(t, "").mounts()
	if err != nil {
		t.Fatal(err)
	}
	if len(off.Git) != 0 {
		t.Fatalf("plan git shares = %+v, want none for a default source", off.Git)
	}
}

// TestNoGitKillSwitch: SANDBOXER_NO_GIT=1 forces every source back to the
// default, whatever the profile says — the operator's word beats the config's,
// like SANDBOXER_NO_EGRESS over egress.enabled.
func TestNoGitKillSwitch(t *testing.T) {
	tg := gitShareTarget(t, config.GitRW)
	t.Setenv("SANDBOXER_NO_GIT", "1")
	mp, err := tg.mounts()
	if err != nil {
		t.Fatal(err)
	}
	if len(mp.Git) != 0 {
		t.Fatalf("plan git shares = %+v, want none under SANDBOXER_NO_GIT=1", mp.Git)
	}
	// And the source line says so, rather than promising git that is not there.
	line := srcLine(tg.base.Srcs("s")[0])
	if !strings.Contains(line, "SANDBOXER_NO_GIT") {
		t.Errorf("srcLine = %q, want the kill-switch named", line)
	}
}

// TestSrcLineNamesTheGitShare keeps the sandbox's reach visible where the rest
// of it is reported — a shared git dir is as much a property of what the agent
// can touch as an adopted worktree.
func TestSrcLineNamesTheGitShare(t *testing.T) {
	shared := srcLine(sandbox.Source{
		RepoRoot: "/repo", Path: "/wt/repo", Branch: "feat/x", Managed: true,
		Git: config.GitRO, GitDir: "/repo/.git",
	})
	if !strings.Contains(shared, "git:ro") {
		t.Errorf("srcLine = %q, want it to name the git share", shared)
	}
	plain := srcLine(sandbox.Source{RepoRoot: "/repo", Path: "/wt/repo", Branch: "feat/x", Managed: true})
	if strings.Contains(plain, "git:") {
		t.Errorf("srcLine = %q, want no git note for a default source", plain)
	}
}
