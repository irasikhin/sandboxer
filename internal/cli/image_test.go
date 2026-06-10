package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/sandbox"
)

// TestResolveImage covers image selection: nil/empty profiles use the default
// image with no tools, a `tools:` profile resolves to a variant tag plus the
// nixpkgs attrs, and an unknown pack errors.
func TestResolveImage(t *testing.T) {
	def := config.LoadDefaults().Image

	for _, prof := range []*config.Profile{nil, {}} {
		img, tools, err := resolveImage(prof)
		if err != nil || img != def || tools != nil {
			t.Errorf("profile %v → img=%q tools=%v err=%v; want default+nil", prof, img, tools, err)
		}
	}

	img, tools, err := resolveImage(&config.Profile{Tools: []string{"go"}})
	if err != nil {
		t.Fatal(err)
	}
	if img == def {
		t.Error("a tools profile must use a variant image, not the default")
	}
	if len(tools) != 1 || tools[0] != "go" {
		t.Errorf("resolved attrs = %v, want [go]", tools)
	}

	if _, _, err := resolveImage(&config.Profile{Tools: []string{"nope"}}); err == nil {
		t.Error("unknown tool pack must error")
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
