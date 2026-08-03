package toolbox

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/registry"
)

// Spec is the fully resolved description of a toolbox image variant: every
// input that changes the image's content, normalized so equal customizations
// hash to the same content-addressed tag. The zero Spec is the stock default
// image.
type Spec struct {
	// Attrs are the nixpkgs attribute names baked into the image — the union
	// of the profile's resolved `tools:` packs and `image.extraPkgs` — sorted
	// and deduplicated. Dotted attribute paths (python3Packages.requests) are
	// allowed.
	Attrs []string
	// OverlayFile is the path to the profile's nixpkgs-overlay file ("" =
	// none). After any profile load config guarantees it is absolute, so the
	// spec stays valid from any working directory.
	OverlayFile string
	// OverlaySHA is the sha256 hex of OverlayFile's content ("" when unset) —
	// the overlay's contribution to the content-addressed tag.
	OverlaySHA string
	// Files/Env are the profile's static image customization (image.files /
	// image.env), rendered into the build context and folded into the tag.
	Files map[string]string
	Env   map[string]string
	// LLMAgentsRev / NixpkgsRev select the flake-input revs. "" and "latest"
	// both mean "track the remote head" (the default — agents auto-update on
	// `image build`, which stamps the resolved rev into the pins cache); a
	// full 40-hex commit pins the input exactly. A tracking rev must be
	// resolved to a commit before the spec is tagged or built (see PinSpec /
	// effectiveRev).
	LLMAgentsRev string
	NixpkgsRev   string
}

// isLatestRev reports whether a rev tracks the remote head rather than
// pinning a commit: the empty default and the explicit "latest" spelling are
// the same thing (auto-update is the default, "latest" just writes it down).
func isLatestRev(rev string) bool {
	return rev == "" || rev == "latest"
}

// ResolveSpec resolves a profile's image customization (`tools:` packs,
// image.extraPkgs, the user nix hook, rev overrides) into a Spec. A nil
// profile or one without customization yields the empty Spec. Fail-closed: an
// unknown tool pack or a missing user nix file is an error here, before any
// container work starts.
func ResolveSpec(p *config.Profile) (Spec, error) {
	if p == nil {
		return Spec{}, nil
	}
	attrs, err := registry.ResolveTools(p.Tools)
	if err != nil {
		return Spec{}, err
	}
	seen := make(map[string]bool, len(attrs))
	for _, a := range attrs {
		seen[a] = true
	}
	for _, a := range p.Image.Packages {
		if !seen[a] {
			seen[a] = true
			attrs = append(attrs, a)
		}
	}
	slices.Sort(attrs)
	if err := config.ValidateImageSpec(p.Image); err != nil {
		return Spec{}, err
	}
	s := Spec{
		Attrs:        attrs,
		OverlayFile:  p.Image.Overlay,
		Files:        p.Image.Files,
		Env:          p.Image.Env,
		LLMAgentsRev: p.Image.LLMAgentsRev,
		NixpkgsRev:   p.Image.NixpkgsRev,
	}
	if s.OverlayFile != "" {
		data, err := os.ReadFile(s.OverlayFile)
		if err != nil {
			return Spec{}, fmt.Errorf("image.overlay: %w — fix the profile's image.overlay path", err)
		}
		sum := sha256.Sum256(data)
		s.OverlaySHA = hex.EncodeToString(sum[:])
	}
	return s, nil
}

// Empty reports whether the spec requests no customization — the sandbox runs
// the stock default image. A tracking rev ("" or "latest") is not a
// customization: it IS the stock image's behavior, so writing it down must
// not divert the profile to a variant tag. Only a concrete pin does.
func (s Spec) Empty() bool {
	return len(s.Attrs) == 0 && s.OverlayFile == "" && len(s.Files) == 0 &&
		len(s.Env) == 0 && isLatestRev(s.LLMAgentsRev) && isLatestRev(s.NixpkgsRev)
}

// Tag returns the image reference for the spec: the default image when empty,
// otherwise a content-addressed "sandboxer-toolbox:var-<12 hex>" over every
// content input (effective input revs, baked attrs, files, env, the overlay
// file's hash), so identical customizations share one cached image and any
// change — a package, a file's text, the overlay's content, a pin bump —
// yields a new tag.
func (s Spec) Tag() string {
	if s.Empty() {
		return config.DefaultImage
	}
	overlaySHA := s.OverlaySHA
	if overlaySHA == "" {
		overlaySHA = "-"
	}
	// Every list is NUL-joined so adjacent values can never collide by
	// concatenation (["go,rg"] vs ["go","rg"]) — the session ConfigHash
	// convention; maps are serialized in sorted key order.
	sum := sha256.Sum256([]byte("v2\x00" +
		"nixpkgs=" + effectiveRev("nixpkgs", s.NixpkgsRev) + "\x00" +
		"llm-agents=" + effectiveRev("llm-agents", s.LLMAgentsRev) + "\x00" +
		"attrs=" + strings.Join(s.Attrs, "\x00") + "\x00" +
		"files=" + joinSortedKV(s.Files) + "\x00" +
		"env=" + joinSortedKV(s.Env) + "\x00" +
		"overlay=" + overlaySHA))
	return "sandboxer-toolbox:var-" + hex.EncodeToString(sum[:])[:12]
}

// joinSortedKV serializes a string map deterministically for hashing.
func joinSortedKV(m map[string]string) string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(m)) {
		b.WriteString(k)
		b.WriteByte(0)
		b.WriteString(m[k])
		b.WriteByte(0)
	}
	return b.String()
}

// effectiveRev is the input rev a build of the spec would actually use. Only a
// concrete commit may reach a tag: an unresolved tracking rev ("" or "latest")
// here is a sequencing bug — PinSpec must run before tagging — and panics in
// this package's fail-loud style rather than minting a tag that can never be
// content-stable.
func effectiveRev(input, rev string) string {
	if isLatestRev(rev) {
		panic(fmt.Sprintf("toolbox: unresolved %q %s rev at tag time — resolve pins (PinSpec) before tagging", rev, input))
	}
	return rev
}
