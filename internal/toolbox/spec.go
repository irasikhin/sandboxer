package toolbox

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
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
	// NixFile is the path to the profile's user nix hook ("" = none). After
	// any profile load config guarantees it is absolute, so the spec stays
	// valid from any working directory.
	NixFile string
	// NixSHA is the sha256 hex of NixFile's content ("" when NixFile is "") —
	// the file's contribution to the content-addressed tag.
	NixSHA string
	// LLMAgentsRev / NixpkgsRev override the embedded flake-input pins; ""
	// keeps the embedded pin. A literal "latest" must be resolved to a commit
	// before the spec is tagged or built (see effectiveRev).
	LLMAgentsRev string
	NixpkgsRev   string
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
	for _, a := range p.Image.ExtraPkgs {
		if !seen[a] {
			seen[a] = true
			attrs = append(attrs, a)
		}
	}
	sort.Strings(attrs)
	s := Spec{
		Attrs:        attrs,
		NixFile:      p.Image.Nix,
		LLMAgentsRev: p.Image.LLMAgentsRev,
		NixpkgsRev:   p.Image.NixpkgsRev,
	}
	if s.NixFile != "" {
		data, err := os.ReadFile(s.NixFile)
		if err != nil {
			return Spec{}, fmt.Errorf("image.nix: %w", err)
		}
		sum := sha256.Sum256(data)
		s.NixSHA = hex.EncodeToString(sum[:])
	}
	return s, nil
}

// Empty reports whether the spec requests no customization — the sandbox runs
// the stock default image.
func (s Spec) Empty() bool {
	return len(s.Attrs) == 0 && s.NixFile == "" && s.LLMAgentsRev == "" && s.NixpkgsRev == ""
}

// Tag returns the image reference for the spec: the default image when empty,
// otherwise a content-addressed "sandboxer-toolbox:var-<12 hex>" over every
// content input (effective input revs, baked attrs, the user nix file's hash),
// so identical customizations share one cached image and any change — a
// package, the nix hook's content, a pin bump — yields a new tag.
func (s Spec) Tag() string {
	if s.Empty() {
		return config.DefaultImage
	}
	embNixpkgs, embLLMAgents := EmbeddedRevs()
	nixSHA := s.NixSHA
	if nixSHA == "" {
		nixSHA = "-"
	}
	// Attrs are NUL-joined so adjacent names can never collide by
	// concatenation (["go,rg"] vs ["go","rg"]) — the session ConfigHash
	// convention.
	sum := sha256.Sum256([]byte("v1\x00" +
		"nixpkgs=" + effectiveRev("nixpkgs", s.NixpkgsRev, embNixpkgs) + "\x00" +
		"llm-agents=" + effectiveRev("llm-agents", s.LLMAgentsRev, embLLMAgents) + "\x00" +
		"attrs=" + strings.Join(s.Attrs, "\x00") + "\x00" +
		"nix=" + nixSHA))
	return "sandboxer-toolbox:var-" + hex.EncodeToString(sum[:])[:12]
}

// effectiveRev is the input rev a build of the spec would actually use: the
// override when set, the embedded pin otherwise. A literal "latest" reaching
// here is a sequencing bug — pin resolution must happen before tagging — and
// panics in this package's fail-loud style rather than minting a tag that can
// never be content-stable.
func effectiveRev(input, override, embedded string) string {
	if override == "" {
		return embedded
	}
	if override == "latest" {
		panic(fmt.Sprintf("toolbox: unresolved %q %s rev at tag time — resolve pins before tagging", override, input))
	}
	return override
}
