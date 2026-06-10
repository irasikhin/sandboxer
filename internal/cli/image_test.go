package cli

import (
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
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
