package toolbox

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
)

// TestResolveSpecEmpty: a nil or customization-free profile resolves to the
// empty spec, whose tag is the stock default image.
func TestResolveSpecEmpty(t *testing.T) {
	for _, p := range []*config.Profile{nil, {}} {
		s, err := ResolveSpec(p)
		if err != nil || !s.Empty() {
			t.Errorf("ResolveSpec(%v) = %+v, %v; want empty spec", p, s, err)
		}
	}
	if got := (Spec{}).Tag(); got != config.DefaultImage {
		t.Errorf("empty spec tag = %q, want %q", got, config.DefaultImage)
	}
}

// TestResolveSpecMerge: tools-pack attrs and image.extraPkgs are unioned,
// deduplicated and sorted, so the attr order in the profile never changes the
// content address.
func TestResolveSpecMerge(t *testing.T) {
	s, err := ResolveSpec(&config.Profile{
		Tools: []string{"go"},
		Image: config.ImageSpec{ExtraPkgs: []string{"ripgrep", "go", "python3Packages.requests"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"go", "python3Packages.requests", "ripgrep"}
	if !slices.Equal(s.Attrs, want) {
		t.Errorf("Attrs = %v, want sorted deduped union %v", s.Attrs, want)
	}

	if _, err := ResolveSpec(&config.Profile{Tools: []string{"nope"}}); err == nil {
		t.Error("unknown tool pack must error")
	}
}

// TestResolveSpecNixFile: the user nix hook is hashed into the spec, a content
// change flips both the hash and the tag, and a missing file is a fail-closed
// error before any container work.
func TestResolveSpecNixFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "image.nix")
	if err := os.WriteFile(f, []byte("{ pkgs }: { packages = [ pkgs.hello ]; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prof := &config.Profile{Image: config.ImageSpec{Nix: f}}
	s1, err := ResolveSpec(prof)
	if err != nil {
		t.Fatal(err)
	}
	if s1.NixFile != f || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(s1.NixSHA) {
		t.Errorf("spec = %+v, want NixFile %q with a 64-hex NixSHA", s1, f)
	}

	if err := os.WriteFile(f, []byte("{ pkgs }: { }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s2, err := ResolveSpec(prof)
	if err != nil {
		t.Fatal(err)
	}
	if s2.NixSHA == s1.NixSHA || s2.Tag() == s1.Tag() {
		t.Error("changing the hook file's content must change NixSHA and the tag")
	}

	_, err = ResolveSpec(&config.Profile{Image: config.ImageSpec{Nix: filepath.Join(dir, "missing.nix")}})
	if err == nil || !strings.Contains(err.Error(), "image.nix") {
		t.Errorf("missing image.nix = %v, want a fail-closed error naming image.nix", err)
	}
}

// TestSpecTag pins the content-addressed tag: deterministic, 12-hex var- form,
// sensitive to every content input, and insensitive to a rev override that
// equals the embedded pin (same effective rev → same image).
func TestSpecTag(t *testing.T) {
	tagRe := regexp.MustCompile(`^sandboxer-toolbox:var-[0-9a-f]{12}$`)
	a := Spec{Attrs: []string{"go", "ripgrep"}}
	if !tagRe.MatchString(a.Tag()) {
		t.Errorf("tag form = %q, want var-<12 hex>", a.Tag())
	}
	if a.Tag() != (Spec{Attrs: []string{"go", "ripgrep"}}).Tag() {
		t.Error("tag must be deterministic for the same spec")
	}
	if a.Tag() == (Spec{Attrs: []string{"go"}}).Tag() {
		t.Error("different attr sets must produce different tags")
	}
	if a.Tag() == (Spec{Attrs: []string{"go", "ripgrep"}, NixSHA: "deadbeef"}).Tag() {
		t.Error("a user nix hash must change the tag")
	}
	// NUL-joined attrs: adjacent names never collide by concatenation, so a
	// bogus comma-joined attr can't alias an already-built valid image and
	// dodge the flake's fail-closed unknown-attribute throw.
	if a.Tag() == (Spec{Attrs: []string{"go,ripgrep"}}).Tag() {
		t.Error(`Attrs ["go,ripgrep"] must not hash like ["go","ripgrep"]`)
	}

	embNixpkgs, embLLMAgents := EmbeddedRevs()
	pinned := Spec{Attrs: []string{"go", "ripgrep"}, NixpkgsRev: embNixpkgs, LLMAgentsRev: embLLMAgents}
	if pinned.Tag() != a.Tag() {
		t.Error("rev overrides equal to the embedded pins must not change the tag")
	}
	if (Spec{Attrs: []string{"go", "ripgrep"}, NixpkgsRev: "deadbeef"}).Tag() == a.Tag() {
		t.Error("a differing nixpkgs rev must change the tag")
	}
	if (Spec{Attrs: []string{"go", "ripgrep"}, LLMAgentsRev: "deadbeef"}).Tag() == a.Tag() {
		t.Error("a differing llm-agents rev must change the tag")
	}
}

// TestSpecTagPanicsOnLatest pins the sequencing contract: an unresolved
// "latest" rev can never be content-addressed, so tagging it is a fail-loud
// bug, not a tag.
func TestSpecTagPanicsOnLatest(t *testing.T) {
	for _, s := range []Spec{
		{Attrs: []string{"go"}, NixpkgsRev: "latest"},
		{Attrs: []string{"go"}, LLMAgentsRev: "latest"},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Tag(%+v) did not panic on an unresolved latest rev", s)
				}
			}()
			_ = s.Tag()
		}()
	}
}

// TestFlakeUserContract guards the embedded flake's user-hook wiring: the
// unconditional ./user.nix import, the two-phase overlay re-import, the
// fail-closed unknown-key validation, and the packages/files/env plumbing into
// the image.
func TestFlakeUserContract(t *testing.T) {
	data, err := assets.ReadFile("assets/flake.nix")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{
		"import ./user.nix",
		"overlays = [",
		`throw "sandboxer: image.nix returned unknown keys`,
		"userPkgs",
		"userFiles",
		"userEnv",
		"writeTextDir",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("embedded flake.nix missing %q — user image contract not wired", want)
		}
	}
}
