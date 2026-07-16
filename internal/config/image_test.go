package config

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestImageSpecEmpty(t *testing.T) {
	cases := []struct {
		name string
		s    ImageSpec
		want bool
	}{
		{"zero", ImageSpec{}, true},
		{"packages", ImageSpec{Packages: []string{"ripgrep"}}, false},
		{"nix", ImageSpec{Overlay: "/abs/image.nix"}, false},
		{"llmAgentsRev", ImageSpec{LLMAgentsRev: "latest"}, false},
		{"nixpkgsRev", ImageSpec{NixpkgsRev: "abcdef0"}, false},
	}
	for _, c := range cases {
		if got := c.s.Empty(); got != c.want {
			t.Errorf("%s: Empty() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestValidateImageSpec(t *testing.T) {
	cases := []struct {
		name string
		s    ImageSpec
		ok   bool
	}{
		{"empty spec", ImageSpec{}, true},
		{"both empty revs", ImageSpec{Packages: []string{"ripgrep"}, Overlay: "/x.nix"}, true},
		{"latest llm-agents", ImageSpec{LLMAgentsRev: "latest"}, true},
		{"latest nixpkgs", ImageSpec{NixpkgsRev: "latest"}, true},
		{"40-char hex", ImageSpec{LLMAgentsRev: strings.Repeat("a1", 20)}, true},
		// Short prefixes are rejected: 7- and 40-hex of the same commit would
		// mint two variant tags, and nix resolves a short github: rev via the
		// GitHub API — a network dependency a pin must not have.
		{"7-char hex too short", ImageSpec{NixpkgsRev: "abcdef0"}, false},
		{"39-char hex too short", ImageSpec{NixpkgsRev: strings.Repeat("a", 39)}, false},
		{"41-char hex too long", ImageSpec{NixpkgsRev: strings.Repeat("a", 41)}, false},
		{"uppercase hex rejected", ImageSpec{LLMAgentsRev: strings.Repeat("A", 40)}, false},
		{"branch name rejected", ImageSpec{LLMAgentsRev: "main"}, false},
		{"non-hex rejected", ImageSpec{NixpkgsRev: strings.Repeat("g", 40)}, false},
	}
	for _, c := range cases {
		err := ValidateImageSpec(c.s)
		if (err == nil) != c.ok {
			t.Errorf("%s: ValidateImageSpec err=%v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestResolveRuntimeRejectsBadImageRev(t *testing.T) {
	// Every entry point goes through ResolveRuntime, so a rev typo must fail
	// early there, not deep inside a build.
	bad := &Profile{Image: ImageSpec{NixpkgsRev: "not-a-rev"}}
	if _, err := ResolveRuntime(bad, Defaults{}, "base.com", Overrides{}); err == nil {
		t.Error("ResolveRuntime should reject an invalid image rev")
	}
}

func TestImageStrictDecode(t *testing.T) {
	dir := t.TempDir()

	// Nested image keys are accepted in the flat form...
	good := writeFile(t, dir, "good.nix", `{
  image = {
    packages = [ "ripgrep" "nodePackages.pnpm" ];
    overlay = "overlay.nix";
    llmAgentsRev = "latest";
    nixpkgsRev = "abcdef0";
  };
}
`)
	p, err := loadFlat(t, good)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Image.Packages, []string{"ripgrep", "nodePackages.pnpm"}) {
		t.Errorf("Packages = %v", p.Image.Packages)
	}
	if p.Image.LLMAgentsRev != "latest" || p.Image.NixpkgsRev != "abcdef0" {
		t.Errorf("revs = %+v", p.Image)
	}

	// ...and inside a profiles section.
	multi := writeFile(t, dir, "multi.nix", "{ profiles.x.image.packages = [ \"jq\" ]; }\n")
	d, err := LoadDocument(multi)
	if err != nil {
		t.Fatal(err)
	}
	x, err := d.Select("x")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(x.Image.Packages, []string{"jq"}) {
		t.Errorf("multi Packages = %v", x.Image.Packages)
	}

	// A typo inside image is rejected by the strict decoder in both forms.
	badFlat := writeFile(t, dir, "bad-flat.nix", "{ image.extraPackages = [ \"jq\" ]; }\n")
	if _, err := LoadDocument(badFlat); err == nil {
		t.Error("flat: unknown image key must be rejected (strict decode)")
	}
	badMulti := writeFile(t, dir, "bad-multi.nix", "{ profiles.x.image.nixRev = \"abc\"; }\n")
	if _, err := LoadDocument(badMulti); err == nil {
		t.Error("multi: unknown image key must be rejected (strict decode)")
	}
}

// TestImageNixPathResolution pins the Load/LoadDocument contract that image.nix
// is stored ABSOLUTE (resolved against the profile file's directory), so the
// _meta/<slug>.profile.json snapshot stays self-contained.
func TestImageNixPathResolution(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		nix  string // value written into the config
		want string // expected after load
	}{
		{"relative", "image.nix", filepath.Join(dir, "image.nix")},
		{"relative subdir", "nix/image.nix", filepath.Join(dir, "nix", "image.nix")},
		{"absolute passthrough", "/abs/image.nix", "/abs/image.nix"},
	}
	for _, c := range cases {
		f := writeFile(t, dir, Sanitize(c.name)+".nix", "{ image.overlay = \""+c.nix+"\"; }\n")
		sel, err := loadFlat(t, f)
		if err != nil {
			t.Fatalf("%s: load: %v", c.name, err)
		}
		if sel.Image.Overlay != c.want {
			t.Errorf("%s: Nix = %q, want %q", c.name, sel.Image.Overlay, c.want)
		}
	}

	// Empty stays empty (no-op, not resolved to the profile dir).
	empty := writeFile(t, dir, "empty.nix", "{ backend = \"docker\"; }\n")
	p, err := loadFlat(t, empty)
	if err != nil {
		t.Fatal(err)
	}
	if p.Image.Overlay != "" {
		t.Errorf("empty Nix should stay empty, got %q", p.Image.Overlay)
	}
}

func TestImageNixPathResolutionMulti(t *testing.T) {
	// Every section of a multi document is resolved, so each profile's relative
	// image.nix becomes an absolute path anchored at the file's directory.
	dir := t.TempDir()
	body := `{
  profiles = {
    web = { backend = "podman"; image.overlay = "web.nix"; };
    api = { backend = "docker"; image.overlay = "base.nix"; };
  };
}
`
	f := writeFile(t, dir, ConfigFileName, body)
	d, err := LoadDocument(f)
	if err != nil {
		t.Fatal(err)
	}
	web, err := d.Select("web")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "web.nix"); web.Image.Overlay != want {
		t.Errorf("web Nix = %q, want %q", web.Image.Overlay, want)
	}
	api, err := d.Select("api")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "base.nix"); api.Image.Overlay != want {
		t.Errorf("api Nix = %q, want %q", api.Image.Overlay, want)
	}
}

// loadFlat evaluates a flat config file and selects its single profile — the
// nix-era stand-in for the retired flat Load helper.
func loadFlat(t *testing.T, path string) (*Profile, error) {
	t.Helper()
	d, err := LoadDocument(path)
	if err != nil {
		return nil, err
	}
	return d.Select("")
}
