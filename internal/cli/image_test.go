package cli

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/toolbox"
)

// TestResolveImage covers image selection: nil/empty/tracking-only profiles
// use the default image with an empty spec, any content customization
// (`tools:` or `image:`) resolves to the spec's content-addressed variant tag
// (revs from the warm pins stamp), and resolution failures (unknown pack,
// missing image.nix) error out.
func TestResolveImage(t *testing.T) {
	def := config.LoadDefaults().Image
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	rev := strings.Repeat("a", 40)
	if err := toolbox.SavePins(toolbox.Pins{
		"nixpkgs":    {Ref: "refs/heads/nixos-unstable", Rev: rev},
		"llm-agents": {Ref: "HEAD", Rev: rev},
	}); err != nil {
		t.Fatal(err)
	}

	for _, prof := range []*config.Profile{
		nil,
		{},
		{Image: config.ImageSpec{LLMAgentsRev: "latest"}},
	} {
		img, spec, err := resolveImage(prof, "", io.Discard)
		if err != nil || img != def || !spec.Empty() {
			t.Errorf("profile %v → img=%q spec=%+v err=%v; want default+empty", prof, img, spec, err)
		}
	}

	img, spec, err := resolveImage(&config.Profile{Tools: []string{"go"}}, "", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if img == def || !strings.HasPrefix(img, "sandboxer-toolbox:var-") {
		t.Errorf("a tools profile must use a var- variant image, got %q", img)
	}
	if len(spec.Attrs) != 1 || spec.Attrs[0] != "go" {
		t.Errorf("resolved attrs = %v, want [go]", spec.Attrs)
	}

	// image.packages alone selects a variant too, and the reference is the
	// spec's own content address (the backend rebuilds from the same spec).
	img2, spec2, err := resolveImage(&config.Profile{Image: config.ImageSpec{Packages: []string{"ripgrep"}}}, "", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if img2 != spec2.Tag() {
		t.Errorf("image %q != spec tag %q", img2, spec2.Tag())
	}

	if _, _, err := resolveImage(&config.Profile{Tools: []string{"nope"}}, "", io.Discard); err == nil {
		t.Error("unknown tool pack must error")
	}
	if _, _, err := resolveImage(&config.Profile{
		Image: config.ImageSpec{Overlay: filepath.Join(t.TempDir(), "missing.nix")},
	}, "", io.Discard); err == nil {
		t.Error("missing image.nix must error")
	}
}

// TestResolveImageLatest covers the tracking-rev plumbing in resolveImage: a
// variant with the tracking default and a cold pins cache and no engine fails
// with build-image guidance; a warm stamp resolves without any engine and the
// tag is the pinned spec's own content address. A tracking-only profile never
// needs the stamp at all — it is the stock image.
func TestResolveImageLatest(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	prof := &config.Profile{Tools: []string{"go"}}

	if _, _, err := resolveImage(prof, "", io.Discard); err == nil ||
		!strings.Contains(err.Error(), "image build") {
		t.Errorf("cold cache without an engine = %v, want build-image guidance", err)
	}
	// The stock image needs no pins: a cold cache resolves it fine.
	if img, _, err := resolveImage(&config.Profile{Image: config.ImageSpec{NixpkgsRev: "latest"}}, "", io.Discard); err != nil ||
		img != config.LoadDefaults().Image {
		t.Errorf("tracking-only profile on a cold cache = %q, %v; want the stock default", img, err)
	}

	rev := strings.Repeat("a", 40)
	if err := toolbox.SavePins(toolbox.Pins{
		"nixpkgs":    {Rev: rev},
		"llm-agents": {Rev: rev},
	}); err != nil {
		t.Fatal(err)
	}
	img, spec, err := resolveImage(prof, "", io.Discard)
	if err != nil {
		t.Fatalf("warm cache: %v", err)
	}
	if spec.NixpkgsRev != rev || spec.LLMAgentsRev != rev {
		t.Errorf("revs = %q/%q, want the stamped rev", spec.NixpkgsRev, spec.LLMAgentsRev)
	}
	if img != spec.Tag() || !strings.HasPrefix(img, "sandboxer-toolbox:var-") {
		t.Errorf("image %q != pinned spec tag %q", img, spec.Tag())
	}
}
