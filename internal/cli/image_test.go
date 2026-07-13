package cli

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/toolbox"
)

// TestResolveImage covers image selection: nil/empty profiles use the default
// image with an empty spec, any customization (`tools:` or `image:`) resolves
// to the spec's content-addressed variant tag, and resolution failures
// (unknown pack, missing image.nix) error out.
func TestResolveImage(t *testing.T) {
	def := config.LoadDefaults().Image

	for _, prof := range []*config.Profile{nil, {}} {
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

	// image.extraPkgs alone selects a variant too, and the reference is the
	// spec's own content address (the backend rebuilds from the same spec).
	img2, spec2, err := resolveImage(&config.Profile{Image: config.ImageSpec{ExtraPkgs: []string{"ripgrep"}}}, "", io.Discard)
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
		Image: config.ImageSpec{Nix: filepath.Join(t.TempDir(), "missing.nix")},
	}, "", io.Discard); err == nil {
		t.Error("missing image.nix must error")
	}
}

// TestResolveImageLatest covers the "latest" pin plumbing in resolveImage: a
// cold pins cache with no engine fails with build-image guidance; a warm
// stamp resolves without any engine and the tag is the pinned spec's own
// content address.
func TestResolveImageLatest(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	prof := &config.Profile{Image: config.ImageSpec{NixpkgsRev: "latest"}}

	if _, _, err := resolveImage(prof, "", io.Discard); err == nil ||
		!strings.Contains(err.Error(), "image build") {
		t.Errorf("cold cache without an engine = %v, want build-image guidance", err)
	}

	rev := strings.Repeat("a", 40)
	if err := toolbox.SavePins(toolbox.Pins{"nixpkgs": {Rev: rev}}); err != nil {
		t.Fatal(err)
	}
	img, spec, err := resolveImage(prof, "", io.Discard)
	if err != nil {
		t.Fatalf("warm cache: %v", err)
	}
	if spec.NixpkgsRev != rev {
		t.Errorf("NixpkgsRev = %q, want the stamped rev", spec.NixpkgsRev)
	}
	if img != spec.Tag() || !strings.HasPrefix(img, "sandboxer-toolbox:var-") {
		t.Errorf("image %q != pinned spec tag %q", img, spec.Tag())
	}
}
