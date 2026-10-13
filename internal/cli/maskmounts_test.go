package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/sandbox"
)

// maskTarget records a sandbox whose single source carries the given include
// list, and returns a target over it.
func maskTarget(t *testing.T, include []string) *target {
	t.Helper()
	base, err := sandbox.ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srcs := []sandbox.Source{{
		RepoRoot: "/repo", Path: "/wt/repo", Branch: "feat/x", Managed: true,
		Include: include,
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

// TestMountPlanCarriesMasks: an exclusion becomes a read-only overmount of the
// state dir's shared empty dir at the negated target — and nothing at all (no
// dir, no argv entry) when no source excludes.
func TestMountPlanCarriesMasks(t *testing.T) {
	tg := maskTarget(t, []string{"/src/", "!/src/vendor/"})
	mp, err := tg.mounts()
	if err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(tg.base.Dir, "_empty")
	if !isDir(empty) {
		t.Fatalf("plan masks = %+v, want the shared empty dir %s created", mp.Masks, empty)
	}
	want := config.Mount{Source: empty, Target: filepath.Join("/wt/repo", "src", "vendor"), Mode: "ro"}
	if len(mp.Masks) != 1 || mp.Masks[0] != want {
		t.Errorf("plan masks = %+v, want [%+v]", mp.Masks, want)
	}
	if len(mp.Src) != 1 || mp.Src[0] != filepath.Join("/wt/repo", "src") {
		t.Errorf("plan sources = %v, want the exposed /src", mp.Src)
	}

	plain := maskTarget(t, []string{"/src/"})
	pmp, err := plain.mounts()
	if err != nil {
		t.Fatal(err)
	}
	if pmp.Masks != nil {
		t.Errorf("plan masks = %+v, want nil without a negation", pmp.Masks)
	}
	if _, err := os.Stat(filepath.Join(plain.base.Dir, "_empty")); !os.IsNotExist(err) {
		t.Errorf("a mask-free plan touched _empty (err=%v)", err)
	}
}
