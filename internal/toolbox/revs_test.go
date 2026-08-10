package toolbox

import (
	"regexp"
	"strings"
	"testing"
)

// TestEmbeddedRevsShape guards that the pin parses out of the embedded flake
// as a full 40-hex commit hash — never empty, never a branch name or a
// shortened rev that nix would have to resolve at build time.
func TestEmbeddedRevsShape(t *testing.T) {
	nixpkgs := EmbeddedRevs()
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(nixpkgs) {
		t.Errorf("nixpkgs rev %q is not a 40-hex commit pin", nixpkgs)
	}
}

// TestEmbeddedRevsMatchAsset asserts the accessor returns exactly the pin
// literals present in the embedded asset's input URLs, so the regex can never
// silently drift to matching some other line of the flake.
func TestEmbeddedRevsMatchAsset(t *testing.T) {
	data, err := assets.ReadFile("assets/flake.nix")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	nixpkgs := EmbeddedRevs()
	want := `nixpkgs.url = "github:NixOS/nixpkgs/` + nixpkgs + `";`
	if !strings.Contains(s, want) {
		t.Errorf("embedded flake.nix missing %q — EmbeddedRevs drifted from the asset", want)
	}
}

// TestEmbeddedRevPanicsOnMissingPin pins the fail-loud contract: a flake
// source without the expected input pin is a broken build-time invariant, not
// a value to default.
func TestEmbeddedRevPanicsOnMissingPin(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("embeddedRev did not panic on a flake without the pin")
		}
	}()
	embeddedRev([]byte(`inputs.nixpkgs.url = "github:NixOS/nixpkgs/master";`),
		"nixpkgs", "NixOS/nixpkgs")
}
