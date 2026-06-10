package toolbox

import (
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
)

// TestToolsImageTag pins the per-profile variant tagging: no tools → the default
// image; tools → a deterministic, content-keyed tag that differs per tool set.
func TestToolsImageTag(t *testing.T) {
	if got := ToolsImageTag(nil); got != config.DefaultImage {
		t.Errorf("no tools → %q, want default %q", got, config.DefaultImage)
	}
	a := ToolsImageTag([]string{"go", "nodejs"})
	if a != ToolsImageTag([]string{"go", "nodejs"}) {
		t.Error("tag must be deterministic for the same tool set")
	}
	if a == ToolsImageTag([]string{"go"}) {
		t.Error("different tool sets must produce different tags")
	}
	if !strings.HasPrefix(a, "sandboxer-toolbox:tools-") {
		t.Errorf("unexpected tag form: %q", a)
	}
}

// TestFlakeImportsToolsNix guards that the embedded flake wires the per-profile
// tool pack (imports ./tools.nix and adds the packages to the image).
func TestFlakeImportsToolsNix(t *testing.T) {
	data, err := assets.ReadFile("assets/flake.nix")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{"import ./tools.nix", "toolPkgs"} {
		if !strings.Contains(s, want) {
			t.Errorf("embedded flake.nix missing %q — tool pack not wired", want)
		}
	}
}
