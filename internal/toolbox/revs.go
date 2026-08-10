package toolbox

import (
	"fmt"
	"regexp"
)

// EmbeddedRevs returns the nixpkgs revision pin from the embedded toolbox
// flake (assets/flake.nix) — the flake's single input. It is the effective
// input rev of a default image build (a profile/flag override replaces it
// per-build via --override-input); a guard test keeps it in sync with the
// repo-root flake.lock. The pin is a build-time invariant baked into the
// binary, so a malformed embedded asset panics — mirroring the registry
// package's stance on its embedded JSON.
func EmbeddedRevs() (nixpkgs string) {
	flake, err := assets.ReadFile("assets/flake.nix")
	if err != nil {
		panic("toolbox: embedded assets/flake.nix unreadable: " + err.Error())
	}
	return embeddedRev(flake, "nixpkgs", "NixOS/nixpkgs")
}

// embeddedRev extracts the 40-hex commit pin from the named input's
// `<input>.url = "github:<repo>/<rev>";` line in the embedded flake source.
func embeddedRev(flake []byte, input, repo string) string {
	re := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(input) +
		`\.url\s*=\s*"github:` + regexp.QuoteMeta(repo) + `/([0-9a-f]{40})";`)
	m := re.FindSubmatch(flake)
	if m == nil {
		panic(fmt.Sprintf("toolbox: embedded flake.nix has no %s pin "+
			"(expected %s.url = \"github:%s/<40-hex rev>\")", input, input, repo))
	}
	return string(m[1])
}
