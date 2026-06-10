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
		{"extraPkgs", ImageSpec{ExtraPkgs: []string{"ripgrep"}}, false},
		{"nix", ImageSpec{Nix: "/abs/image.nix"}, false},
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
		{"both empty revs", ImageSpec{ExtraPkgs: []string{"ripgrep"}, Nix: "/x.nix"}, true},
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
	if _, err := ResolveRuntime(bad, Defaults{}, "base.com", "bm", Overrides{}); err == nil {
		t.Error("ResolveRuntime should reject an invalid image rev")
	}
}

func TestImageStrictDecode(t *testing.T) {
	dir := t.TempDir()

	// Nested image: keys are accepted in the flat form...
	good := writeFile(t, dir, "good.yaml", `
image:
  extraPkgs: [ripgrep, nodePackages.pnpm]
  nix: sandbox-image.nix
  llmAgentsRev: latest
  nixpkgsRev: abcdef0
`)
	p, err := Load(good)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Image.ExtraPkgs, []string{"ripgrep", "nodePackages.pnpm"}) {
		t.Errorf("ExtraPkgs = %v", p.Image.ExtraPkgs)
	}
	if p.Image.LLMAgentsRev != "latest" || p.Image.NixpkgsRev != "abcdef0" {
		t.Errorf("revs = %+v", p.Image)
	}

	// ...and inside a profiles: section.
	multi := writeFile(t, dir, "multi.yaml", "profiles:\n  x:\n    image:\n      extraPkgs: [jq]\n")
	d, err := LoadDocument(multi)
	if err != nil {
		t.Fatal(err)
	}
	x, err := d.Select("x")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(x.Image.ExtraPkgs, []string{"jq"}) {
		t.Errorf("multi ExtraPkgs = %v", x.Image.ExtraPkgs)
	}

	// A typo inside image: is rejected by the strict decoder in both forms.
	badFlat := writeFile(t, dir, "bad-flat.yaml", "image:\n  extraPackages: [jq]\n")
	if _, err := Load(badFlat); err == nil {
		t.Error("flat: unknown image key must be rejected (KnownFields)")
	}
	badMulti := writeFile(t, dir, "bad-multi.yaml", "profiles:\n  x:\n    image:\n      nixRev: abc\n")
	if _, err := LoadDocument(badMulti); err == nil {
		t.Error("multi: unknown image key must be rejected (KnownFields)")
	}
}

// TestImageNixPathResolution pins the Load/LoadDocument contract that image.nix
// is stored ABSOLUTE (resolved against the profile file's directory), so the
// _meta/<slug>.profile.json snapshot stays self-contained.
func TestImageNixPathResolution(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		nix  string // value written into the yaml
		want string // expected after load
	}{
		{"relative", "sandbox-image.nix", filepath.Join(dir, "sandbox-image.nix")},
		{"relative subdir", "nix/image.nix", filepath.Join(dir, "nix", "image.nix")},
		{"absolute passthrough", "/abs/image.nix", "/abs/image.nix"},
	}
	for _, c := range cases {
		f := writeFile(t, dir, Sanitize(c.name)+".yaml", "image:\n  nix: "+c.nix+"\n")

		p, err := Load(f)
		if err != nil {
			t.Fatalf("%s: Load: %v", c.name, err)
		}
		if p.Image.Nix != c.want {
			t.Errorf("%s: Load Nix = %q, want %q", c.name, p.Image.Nix, c.want)
		}

		d, err := LoadDocument(f) // flat path of LoadDocument resolves too
		if err != nil {
			t.Fatalf("%s: LoadDocument: %v", c.name, err)
		}
		sel, err := d.Select("")
		if err != nil {
			t.Fatal(err)
		}
		if sel.Image.Nix != c.want {
			t.Errorf("%s: LoadDocument Nix = %q, want %q", c.name, sel.Image.Nix, c.want)
		}
	}

	// Empty stays empty (no-op, not resolved to the profile dir).
	empty := writeFile(t, dir, "empty.yaml", "backend: docker\n")
	p, err := Load(empty)
	if err != nil {
		t.Fatal(err)
	}
	if p.Image.Nix != "" {
		t.Errorf("empty Nix should stay empty, got %q", p.Image.Nix)
	}
}

func TestImageNixPathResolutionMulti(t *testing.T) {
	// Every section of a multi document is resolved — defaults: included — so a
	// profile inheriting defaults' image.nix gets an absolute path too.
	dir := t.TempDir()
	body := `
defaults:
  image:
    nix: base.nix
profiles:
  web:
    image:
      nix: web.nix
  api:
    backend: docker
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
	if want := filepath.Join(dir, "web.nix"); web.Image.Nix != want {
		t.Errorf("web Nix = %q, want %q", web.Image.Nix, want)
	}
	api, err := d.Select("api")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "base.nix"); api.Image.Nix != want {
		t.Errorf("api should inherit defaults' resolved Nix: %q, want %q", api.Image.Nix, want)
	}
}

// TestMergeProfileImagePerField pins the per-field merge semantics: each of the
// four ImageSpec fields overrides independently, so defaults: can pin revs
// while a profile adds extraPkgs without losing them.
func TestMergeProfileImagePerField(t *testing.T) {
	base := ImageSpec{
		ExtraPkgs:    []string{"base-pkg"},
		Nix:          "/base/image.nix",
		LLMAgentsRev: "abcdef0",
		NixpkgsRev:   "1234567",
	}
	cases := []struct {
		name string
		over ImageSpec
		want ImageSpec
	}{
		{"empty over keeps base", ImageSpec{}, base},
		{
			"extraPkgs only",
			ImageSpec{ExtraPkgs: []string{"jq"}},
			ImageSpec{ExtraPkgs: []string{"jq"}, Nix: "/base/image.nix", LLMAgentsRev: "abcdef0", NixpkgsRev: "1234567"},
		},
		{
			"nix only",
			ImageSpec{Nix: "/over/image.nix"},
			ImageSpec{ExtraPkgs: []string{"base-pkg"}, Nix: "/over/image.nix", LLMAgentsRev: "abcdef0", NixpkgsRev: "1234567"},
		},
		{
			"llmAgentsRev only",
			ImageSpec{LLMAgentsRev: "latest"},
			ImageSpec{ExtraPkgs: []string{"base-pkg"}, Nix: "/base/image.nix", LLMAgentsRev: "latest", NixpkgsRev: "1234567"},
		},
		{
			"nixpkgsRev only",
			ImageSpec{NixpkgsRev: "latest"},
			ImageSpec{ExtraPkgs: []string{"base-pkg"}, Nix: "/base/image.nix", LLMAgentsRev: "abcdef0", NixpkgsRev: "latest"},
		},
		{
			"all fields",
			ImageSpec{ExtraPkgs: []string{"jq"}, Nix: "/o.nix", LLMAgentsRev: "latest", NixpkgsRev: "fedcba9"},
			ImageSpec{ExtraPkgs: []string{"jq"}, Nix: "/o.nix", LLMAgentsRev: "latest", NixpkgsRev: "fedcba9"},
		},
	}
	for _, c := range cases {
		got := mergeProfile(Profile{Image: base}, Profile{Image: c.over}).Image
		if !slices.Equal(got.ExtraPkgs, c.want.ExtraPkgs) ||
			got.Nix != c.want.Nix ||
			got.LLMAgentsRev != c.want.LLMAgentsRev ||
			got.NixpkgsRev != c.want.NixpkgsRev {
			t.Errorf("%s: merged Image = %+v, want %+v", c.name, got, c.want)
		}
	}
}
