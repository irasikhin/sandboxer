package sandbox

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// TestID: the handle is derived, stable and per-PROJECT — two projects that
// both hold a "feat" sandbox must not collide, which is the whole reason the
// listing needs an id at all.
func TestID(t *testing.T) {
	a, b := ID("/state/one", "feat"), ID("/state/two", "feat")
	if a != ID("/state/one", "feat") {
		t.Error("ID must be stable for the same (state dir, slug)")
	}
	if a == b {
		t.Errorf("two projects' %q sandboxes share id %s", "feat", a)
	}
	if a == ID("/state/one", "other") {
		t.Error("two slugs in one project share an id")
	}
	if len(a) != IDLen {
		t.Errorf("ID = %q, want %d characters", a, IDLen)
	}
	if !LooksLikeID(a) {
		t.Errorf("ID %q is not accepted by LooksLikeID", a)
	}
}

// TestLooksLikeID: only lowercase hex of at least MinIDPrefix characters may be
// read as a handle — the guard that keeps ordinary slugs from being hijacked by
// the id lookup.
func TestLooksLikeID(t *testing.T) {
	for tok, want := range map[string]bool{
		"":          false,
		"ab":        false, // shorter than MinIDPrefix
		"feat":      false, // not hex
		"beef":      true,  // hex and long enough: a slug like this IS ambiguous
		"deadbeef":  true,
		"deadbeef0": false, // longer than a full id
		"DEADBEEF":  false, // ids are printed lowercase
		"dead-beef": false,
	} {
		if got := LooksLikeID(tok); got != want {
			t.Errorf("LooksLikeID(%q) = %v, want %v", tok, got, want)
		}
	}
}

// TestFindByID resolves a handle host-wide: an exact id, an unambiguous prefix,
// a project whose directory is GONE (the state no cwd can reach), and the two
// failure modes — ambiguous (an error naming the candidates) and unknown (an
// ErrNoSuchID a guessing caller can fall back from).
func TestFindByID(t *testing.T) {
	t.Setenv("SANDBOXER_STATE", t.TempDir())
	live, gone := t.TempDir(), t.TempDir()
	for src, slugs := range map[string][]string{live: {"feat", "docs"}, gone: {"feat"}} {
		base, err := ResolveBase(src)
		if err != nil {
			t.Fatal(err)
		}
		for _, slug := range slugs {
			if err := base.AppendAgent(slug); err != nil {
				t.Fatal(err)
			}
		}
	}
	goneBase, err := ResolveBase(gone)
	if err != nil {
		t.Fatal(err)
	}
	goneID := goneBase.ID("feat")
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	t.Run("exact id in a project whose directory is gone", func(t *testing.T) {
		ref, err := FindByID(goneID)
		if err != nil {
			t.Fatalf("FindByID(%s): %v", goneID, err)
		}
		if ref.Slug != "feat" || ref.Src != gone || !ref.Gone {
			t.Errorf("FindByID = %+v, want feat in the gone project %s", ref, gone)
		}
	})

	t.Run("a unique prefix is enough", func(t *testing.T) {
		ref, err := FindByID(goneID[:MinIDPrefix])
		if err != nil {
			t.Fatalf("FindByID(prefix): %v", err)
		}
		if ref.Slug != "feat" || ref.Src != gone {
			t.Errorf("prefix resolved to %+v, want feat in %s", ref, gone)
		}
	})

	t.Run("an ambiguous prefix names the candidates", func(t *testing.T) {
		_, err := FindByID("")
		if err == nil {
			t.Fatal("the empty prefix matches everything — want an ambiguity error")
		}
		if errors.Is(err, ErrNoSuchID) {
			t.Errorf("ambiguity reported as %v, want a distinct error", err)
		}
		for _, want := range []string{"ambiguous", "feat", "docs"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("ambiguity error %q does not mention %q", err, want)
			}
		}
	})

	t.Run("an unknown id is a fallback-able sentinel", func(t *testing.T) {
		if _, err := FindByID("ffffffff"); !errors.Is(err, ErrNoSuchID) {
			t.Errorf("FindByID(unknown) = %v, want ErrNoSuchID", err)
		}
	})
}
