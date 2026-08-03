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
// empty spec, whose tag is the stock default image. Writing the tracking
// default down ("latest") is not a customization either — it must not divert
// the profile to a variant tag.
func TestResolveSpecEmpty(t *testing.T) {
	for _, p := range []*config.Profile{
		nil,
		{},
		{Image: config.ImageSpec{LLMAgentsRev: "latest", NixpkgsRev: "latest"}},
	} {
		s, err := ResolveSpec(p)
		if err != nil || !s.Empty() {
			t.Errorf("ResolveSpec(%v) = %+v, %v; want empty spec", p, s, err)
		}
	}
	if got := (Spec{}).Tag(); got != config.DefaultImage {
		t.Errorf("empty spec tag = %q, want %q", got, config.DefaultImage)
	}
	// A concrete pin IS a customization: it must hold even when the stock
	// image moves on, so it needs its own content-addressed variant.
	if s := (Spec{NixpkgsRev: strings.Repeat("a", 40)}); s.Empty() {
		t.Error("a concrete rev pin must not be an empty spec")
	}
}

// TestResolveSpecMerge: tools-pack attrs and image.packages are unioned,
// deduplicated and sorted, so the attr order in the profile never changes the
// content address.
func TestResolveSpecMerge(t *testing.T) {
	s, err := ResolveSpec(&config.Profile{
		Tools: []string{"go"},
		Image: config.ImageSpec{Packages: []string{"ripgrep", "go", "python3Packages.requests"}},
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

// TestResolveSpecOverlayFile: the user nix hook is hashed into the spec, a content
// change flips both the hash and the tag, and a missing file is a fail-closed
// error before any container work.
func TestResolveSpecOverlayFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "image.nix")
	if err := os.WriteFile(f, []byte("{ pkgs }: { packages = [ pkgs.hello ]; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prof := &config.Profile{Image: config.ImageSpec{Overlay: f}}
	s1, err := ResolveSpec(prof)
	if err != nil {
		t.Fatal(err)
	}
	if s1.OverlayFile != f || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(s1.OverlaySHA) {
		t.Errorf("spec = %+v, want OverlayFile %q with a 64-hex OverlaySHA", s1, f)
	}

	if err := os.WriteFile(f, []byte("{ pkgs }: { }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s2, err := ResolveSpec(prof)
	if err != nil {
		t.Fatal(err)
	}
	// Tag() needs concrete revs (PinSpec runs before tagging in the real
	// flow); pin both specs identically so only the overlay content differs.
	rev := strings.Repeat("1", 40)
	s1.NixpkgsRev, s1.LLMAgentsRev = rev, rev
	s2.NixpkgsRev, s2.LLMAgentsRev = rev, rev
	if s2.OverlaySHA == s1.OverlaySHA || s2.Tag() == s1.Tag() {
		t.Error("changing the hook file's content must change OverlaySHA and the tag")
	}

	_, err = ResolveSpec(&config.Profile{Image: config.ImageSpec{Overlay: filepath.Join(dir, "missing.nix")}})
	if err == nil || !strings.Contains(err.Error(), "image.overlay") {
		t.Errorf("missing overlay file = %v, want a fail-closed error naming image.overlay", err)
	}
}

// TestSpecTag pins the content-addressed tag: deterministic, 12-hex var- form,
// and sensitive to every content input, the resolved revs included.
func TestSpecTag(t *testing.T) {
	tagRe := regexp.MustCompile(`^sandboxer-toolbox:var-[0-9a-f]{12}$`)
	revN, revL := strings.Repeat("2", 40), strings.Repeat("3", 40)
	pin := func(s Spec) Spec {
		s.NixpkgsRev, s.LLMAgentsRev = revN, revL
		return s
	}
	a := pin(Spec{Attrs: []string{"go", "ripgrep"}})
	if !tagRe.MatchString(a.Tag()) {
		t.Errorf("tag form = %q, want var-<12 hex>", a.Tag())
	}
	if a.Tag() != pin(Spec{Attrs: []string{"go", "ripgrep"}}).Tag() {
		t.Error("tag must be deterministic for the same spec")
	}
	if a.Tag() == pin(Spec{Attrs: []string{"go"}}).Tag() {
		t.Error("different attr sets must produce different tags")
	}
	if a.Tag() == pin(Spec{Attrs: []string{"go", "ripgrep"}, OverlaySHA: "deadbeef"}).Tag() {
		t.Error("a user nix hash must change the tag")
	}
	// NUL-joined attrs: adjacent names never collide by concatenation, so a
	// bogus comma-joined attr can't alias an already-built valid image and
	// dodge the flake's fail-closed unknown-attribute throw.
	if a.Tag() == pin(Spec{Attrs: []string{"go,ripgrep"}}).Tag() {
		t.Error(`Attrs ["go,ripgrep"] must not hash like ["go","ripgrep"]`)
	}

	other := strings.Repeat("4", 40)
	if (Spec{Attrs: []string{"go", "ripgrep"}, NixpkgsRev: other, LLMAgentsRev: revL}).Tag() == a.Tag() {
		t.Error("a differing nixpkgs rev must change the tag")
	}
	if (Spec{Attrs: []string{"go", "ripgrep"}, NixpkgsRev: revN, LLMAgentsRev: other}).Tag() == a.Tag() {
		t.Error("a differing llm-agents rev must change the tag")
	}
}

// TestSpecTagPanicsOnLatest pins the sequencing contract: an unresolved
// tracking rev — the "" default as much as the explicit "latest" — can never
// be content-addressed, so tagging it is a fail-loud bug, not a tag.
func TestSpecTagPanicsOnLatest(t *testing.T) {
	rev := strings.Repeat("5", 40)
	for _, s := range []Spec{
		{Attrs: []string{"go"}, NixpkgsRev: "latest", LLMAgentsRev: rev},
		{Attrs: []string{"go"}, NixpkgsRev: rev, LLMAgentsRev: "latest"},
		{Attrs: []string{"go"}, NixpkgsRev: rev},
		{Attrs: []string{"go"}},
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

// TestFlakeUserContract guards the embedded flake's customization wiring: the
// unconditional plain-overlay import (applied BEFORE attr resolution, so
// image.packages may name overlay attrs) and the files.json/env.json data
// plumbing into the image.
func TestFlakeUserContract(t *testing.T) {
	data, err := assets.ReadFile("assets/flake.nix")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{
		"overlays = [ (import ./overlay.nix) ]",
		`builtins.fromJSON (builtins.readFile ./files.json)`,
		`builtins.fromJSON (builtins.readFile ./env.json)`,
		"userFiles",
		"userEnv",
		"writeTextDir",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("embedded flake.nix missing %q — image customization contract not wired", want)
		}
	}
}
