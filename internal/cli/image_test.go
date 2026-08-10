package cli

import (
	"io"
	"os"
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
		"nixpkgs": {Ref: "refs/heads/nixos-unstable", Rev: rev},
	}); err != nil {
		t.Fatal(err)
	}

	for _, prof := range []*config.Profile{
		nil,
		{},
		{Image: config.ImageSpec{NixpkgsRev: "latest"}},
	} {
		img, spec, err := resolveImage(prof, io.Discard)
		if err != nil || img != def || !spec.Empty() {
			t.Errorf("profile %v → img=%q spec=%+v err=%v; want default+empty", prof, img, spec, err)
		}
	}

	img, spec, err := resolveImage(&config.Profile{Tools: []string{"go"}}, io.Discard)
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
	img2, spec2, err := resolveImage(&config.Profile{Image: config.ImageSpec{Packages: []string{"ripgrep"}}}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if img2 != spec2.Tag() {
		t.Errorf("image %q != spec tag %q", img2, spec2.Tag())
	}

	if _, _, err := resolveImage(&config.Profile{Tools: []string{"nope"}}, io.Discard); err == nil {
		t.Error("unknown tool pack must error")
	}
	if _, _, err := resolveImage(&config.Profile{
		Image: config.ImageSpec{Overlay: filepath.Join(t.TempDir(), "missing.nix")},
	}, io.Discard); err == nil {
		t.Error("missing image.nix must error")
	}
}

// fakeGitRevs installs a `git` shim (prepended to PATH, so a fake engine on
// PATH still resolves) that answers `git ls-remote` for the two flake inputs
// with the given revs, so pin resolution via host git is driven deterministically
// without real git or network.
func fakeGitRevs(t *testing.T, nixpkgsRev string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"case \"$2\" in\n" +
		"  *NixOS/nixpkgs) echo '" + nixpkgsRev + "\trefs/heads/nixos-unstable';;\n" +
		"esac\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

// failingGit installs a `git` shim that exits non-zero, so the host-git pin
// resolver's failure path is driven deterministically.
func failingGit(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

// TestResolveImageLatest covers the tracking-rev plumbing in resolveImage: a
// variant with the tracking default and a COLD pins cache resolves via host git
// (the phase-A acceptance — no engine anywhere), a warm stamp resolves without
// any git at all, and the tag is the pinned spec's own content address. A
// tracking-only profile never needs the stamp — it is the stock image.
func TestResolveImageLatest(t *testing.T) {
	prof := &config.Profile{Tools: []string{"go"}}

	// Cold cache → host git resolves the tracking default (no engine, no
	// pre-stamped pins — the docker-less first-build case).
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	rev := strings.Repeat("a", 40)
	fakeGitRevs(t, rev)
	img, spec, err := resolveImage(prof, io.Discard)
	if err != nil {
		t.Fatalf("cold cache: %v", err)
	}
	if spec.NixpkgsRev != rev {
		t.Errorf("rev = %q, want the resolved rev", spec.NixpkgsRev)
	}
	if img != spec.Tag() || !strings.HasPrefix(img, "sandboxer-toolbox:var-") {
		t.Errorf("image %q != pinned spec tag %q", img, spec.Tag())
	}

	// The stock image needs no pins: a cold cache resolves it fine.
	if img, _, err := resolveImage(&config.Profile{Image: config.ImageSpec{NixpkgsRev: "latest"}}, io.Discard); err != nil ||
		img != config.LoadDefaults().Image {
		t.Errorf("tracking-only profile on a cold cache = %q, %v; want the stock default", img, err)
	}

	// Warm cache hit: no git run at all, revs from the stamp.
	if err := toolbox.SavePins(toolbox.Pins{
		"nixpkgs": {Rev: rev},
	}); err != nil {
		t.Fatal(err)
	}
	img, spec, err = resolveImage(prof, io.Discard)
	if err != nil {
		t.Fatalf("warm cache: %v", err)
	}
	if spec.NixpkgsRev != rev {
		t.Errorf("rev = %q, want the stamped rev", spec.NixpkgsRev)
	}
	if img != spec.Tag() || !strings.HasPrefix(img, "sandboxer-toolbox:var-") {
		t.Errorf("image %q != pinned spec tag %q", img, spec.Tag())
	}
}
