package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/sandbox"
)

// TestResolveImage covers image selection: nil/empty profiles use the default
// image with an empty spec, any customization (`tools:` or `image:`) resolves
// to the spec's content-addressed variant tag, and resolution failures
// (unknown pack, missing image.nix) error out.
func TestResolveImage(t *testing.T) {
	def := config.LoadDefaults().Image

	for _, prof := range []*config.Profile{nil, {}} {
		img, spec, err := resolveImage(prof)
		if err != nil || img != def || !spec.Empty() {
			t.Errorf("profile %v → img=%q spec=%+v err=%v; want default+empty", prof, img, spec, err)
		}
	}

	img, spec, err := resolveImage(&config.Profile{Tools: []string{"go"}})
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
	img2, spec2, err := resolveImage(&config.Profile{Image: config.ImageSpec{ExtraPkgs: []string{"ripgrep"}}})
	if err != nil {
		t.Fatal(err)
	}
	if img2 != spec2.Tag() {
		t.Errorf("image %q != spec tag %q", img2, spec2.Tag())
	}

	if _, _, err := resolveImage(&config.Profile{Tools: []string{"nope"}}); err == nil {
		t.Error("unknown tool pack must error")
	}
	if _, _, err := resolveImage(&config.Profile{
		Image: config.ImageSpec{Nix: filepath.Join(t.TempDir(), "missing.nix")},
	}); err == nil {
		t.Error("missing image.nix must error")
	}
}

// TestApplyMCP checks domain folding (deduped), claude seeding, and the
// unsupported-agent note.
func TestApplyMCP(t *testing.T) {
	base, err := sandbox.ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := base.EnsureHome("s"); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer

	// nil profile → no change, no error.
	rt := config.Runtime{Agent: "claude", Domains: []string{"a.com"}}
	if err := applyMCP(&target{base: base, slug: "s", profile: nil}, &rt, &buf); err != nil {
		t.Fatal(err)
	}
	if len(rt.Domains) != 1 {
		t.Errorf("nil profile must not change domains: %v", rt.Domains)
	}

	// claude + mcp → domains folded (deduped), seeded, no note.
	tp := &target{base: base, slug: "s", profile: &config.Profile{MCP: []string{"fetch"}}}
	rt = config.Runtime{Agent: "claude", Domains: []string{"registry.npmjs.org"}}
	if err := applyMCP(tp, &rt, &buf); err != nil {
		t.Fatal(err)
	}
	// fetch's only domain is registry.npmjs.org, already present → no duplicate.
	if got := countOccurrences(rt.Domains, "registry.npmjs.org"); got != 1 {
		t.Errorf("domain duplicated: %v", rt.Domains)
	}
	if strings.Contains(buf.String(), "not yet supported") {
		t.Error("claude is supported — should print no note")
	}

	// non-claude agent → note printed, domains still folded.
	buf.Reset()
	rt = config.Runtime{Agent: "aider", Domains: nil}
	if err := applyMCP(tp, &rt, &buf); err != nil {
		t.Fatal(err)
	}
	if len(rt.Domains) == 0 {
		t.Error("domains must be folded even for an unsupported agent")
	}
	if !strings.Contains(buf.String(), "not yet supported") {
		t.Errorf("expected an in-agent-setup note, got %q", buf.String())
	}
}

func countOccurrences(xs []string, want string) int {
	n := 0
	for _, x := range xs {
		if x == want {
			n++
		}
	}
	return n
}
