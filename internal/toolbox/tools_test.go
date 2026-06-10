package toolbox

import (
	"strings"
	"testing"
)

// TestFlakeImportsToolsNix guards that the embedded flake wires the per-profile
// tool attrs (imports ./tools.nix and adds the packages to the image) and that
// the lookup is fail-closed: an unknown attribute throws instead of being
// silently dropped, and dotted paths resolve via attrByPath.
func TestFlakeImportsToolsNix(t *testing.T) {
	data, err := assets.ReadFile("assets/flake.nix")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{
		"import ./tools.nix",
		"toolPkgs",
		"attrByPath",
		`throw "sandboxer: unknown nixpkgs attribute`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("embedded flake.nix missing %q — tool pack not wired fail-closed", want)
		}
	}
	if strings.Contains(s, "pkgs.${n} or null") {
		t.Error("tools.nix lookup must be fail-closed — `or null` silently drops unknown attrs")
	}
}
