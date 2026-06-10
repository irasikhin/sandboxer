// Package config models the sandboxer profile and resolves runtime settings.
//
// A profile is optional: scalar settings come from flags and SANDBOXER_* env
// vars, and only structured fields (roots/deps vendoring, extraMounts, env)
// require a .sandboxer.yaml file. The resolved profile is serialized to JSON
// (camelCase keys) and stored under .sandboxer/_meta/<slug>.profile.json; that
// JSON is the single artifact the container backend and the srcs package read.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Mount is an extra bind mount for the container backend.
type Mount struct {
	Source string `yaml:"source" json:"source"`
	Target string `yaml:"target" json:"target"`
	Mode   string `yaml:"mode,omitempty" json:"mode,omitempty"`
}

// Network holds the egress allowlist.
type Network struct {
	AllowedDomains []string `yaml:"allowedDomains,omitempty" json:"allowedDomains,omitempty"`
}

// Proxy configures how the sandbox reaches the outside world through a proxy.
//
// HTTP/HTTPS/No are a corporate proxy forwarded to the agent as HTTP(S)_PROXY /
// NO_PROXY: they BYPASS the egress allowlist sidecar (the agent talks to the
// proxy directly and that proxy is trusted to police egress).
//
// Upstream is the opposite trust model: the egress allowlist sidecar stays on
// and chains allowed traffic through this parent proxy, so sandboxer keeps
// enforcing the domain allowlist AND the traffic still leaves via the proxy.
// Upstream is mutually exclusive with HTTP/HTTPS (see ValidateProxy).
type Proxy struct {
	HTTP     string `yaml:"http,omitempty"     json:"http,omitempty"`
	HTTPS    string `yaml:"https,omitempty"    json:"https,omitempty"`
	No       string `yaml:"no,omitempty"       json:"no,omitempty"`
	Upstream string `yaml:"upstream,omitempty" json:"upstream,omitempty"`
}

// ImageSpec customizes the toolbox image a profile's sandbox runs in: extra
// nixpkgs packages, a user nix file hooked into the image build, and overrides
// for the pinned flake-input revisions. An empty spec means the stock image.
type ImageSpec struct {
	// ExtraPkgs are nixpkgs attribute names baked into the image (dotted
	// attribute paths like nodePackages.pnpm are allowed).
	ExtraPkgs []string `yaml:"extraPkgs,omitempty" json:"extraPkgs,omitempty"`
	// Nix is a user nix file imported by the image build. It may be written
	// relative to the profile file; Load/LoadDocument resolve it to an absolute
	// path so the stored _meta/<slug>.profile.json snapshot stays
	// self-contained.
	Nix string `yaml:"nix,omitempty" json:"nix,omitempty"`
	// LLMAgentsRev / NixpkgsRev override the embedded flake-input pins: empty
	// keeps the embedded pin, "latest" resolves the remote head once at build
	// time, and a full 40-hex commit hash pins exactly (see ValidateImageSpec).
	LLMAgentsRev string `yaml:"llmAgentsRev,omitempty" json:"llmAgentsRev,omitempty"`
	NixpkgsRev   string `yaml:"nixpkgsRev,omitempty"   json:"nixpkgsRev,omitempty"`
}

// Empty reports whether the spec requests no customization, i.e. the sandbox
// runs the stock toolbox image.
func (s ImageSpec) Empty() bool {
	return len(s.ExtraPkgs) == 0 && s.Nix == "" && s.LLMAgentsRev == "" && s.NixpkgsRev == ""
}

var imageRevRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ValidateImageSpec rejects a malformed flake-input revision override. Each rev
// is "" (keep the embedded pin), "latest" (resolve the remote head at build
// time) or a full 40-character lowercase hex commit hash. Short prefixes are
// rejected: the same commit as 7- and 40-hex would mint two different variant
// tags, and nix treats a non-40-hex rev in a github: flakeref as a ref needing
// a GitHub API resolve — a network dependency a pin must not have.
func ValidateImageSpec(s ImageSpec) error {
	for _, r := range []struct{ field, rev string }{
		{"image.llmAgentsRev", s.LLMAgentsRev},
		{"image.nixpkgsRev", s.NixpkgsRev},
	} {
		if r.rev == "" || r.rev == "latest" || imageRevRe.MatchString(r.rev) {
			continue
		}
		return fmt.Errorf("invalid %s %q — use latest or a full 40-char hex commit hash", r.field, r.rev)
	}
	return nil
}

// resolveImageNix makes a relative Image.Nix absolute against dir (the profile
// file's directory), so the path survives being snapshotted to _meta and read
// from another working directory. Absolute paths pass through; an empty field
// is a no-op.
func (p *Profile) resolveImageNix(dir string) {
	nix := p.Image.Nix
	if nix == "" || filepath.IsAbs(nix) {
		return
	}
	nix = filepath.Join(dir, nix)
	if abs, err := filepath.Abs(nix); err == nil {
		nix = abs
	}
	p.Image.Nix = nix
}

// Profile is a sandbox configuration. All fields are optional; an empty profile
// is valid (everything then comes from flags/env/defaults). The sandbox content
// is driven by roots+deps (depsync-style): deps are searched by path suffix
// under roots and copied into the sandbox.
type Profile struct {
	Name        string            `yaml:"name,omitempty"        json:"name,omitempty"`
	Backend     string            `yaml:"backend,omitempty"     json:"backend,omitempty"`
	Agent       string            `yaml:"agent,omitempty"       json:"agent,omitempty"`
	Model       string            `yaml:"model,omitempty"       json:"model,omitempty"`
	Network     Network           `yaml:"network,omitempty"     json:"network,omitempty"`
	Proxy       Proxy             `yaml:"proxy,omitempty"       json:"proxy,omitempty"`
	Agents      []string          `yaml:"agents,omitempty"      json:"agents,omitempty"`
	Egress      *bool             `yaml:"egress,omitempty"      json:"egress,omitempty"`
	Roots       []string          `yaml:"roots,omitempty"       json:"roots,omitempty"`
	Deps        []string          `yaml:"deps,omitempty"        json:"deps,omitempty"`
	ExtraMounts []Mount           `yaml:"extraMounts,omitempty" json:"extraMounts,omitempty"`
	Env         map[string]string `yaml:"env,omitempty"         json:"env,omitempty"`
	// Setup is a one-time shell script run inside the sandbox (bash -lc) before
	// the user/agent takes over — e.g. `npm ci`, a build, a DB seed. It runs
	// once per sandbox (re-run only when the script changes) under the same
	// isolation and egress allowlist as the sandbox.
	Setup string `yaml:"setup,omitempty" json:"setup,omitempty"`
	// Tools names language/runtime tool packs (see registry/tools.json: node,
	// python, go, rust, …) baked into a per-profile toolbox image variant,
	// built on demand and cached by tool-set hash.
	Tools []string `yaml:"tools,omitempty" json:"tools,omitempty"`
	// Image customizes the toolbox image variant this profile's sandbox runs
	// in; an empty spec keeps the stock image. See ImageSpec.
	Image ImageSpec `yaml:"image,omitempty" json:"image,omitempty"`
	// MCP names MCP servers (see registry/mcp.json) to wire into the agent: the
	// server config is seeded into the agent's sandbox home and each server's
	// domains are folded into the egress allowlist.
	MCP []string `yaml:"mcp,omitempty" json:"mcp,omitempty"`
	// Session selects how enter/exec use the container: "persistent" (the
	// default) keeps one detached session container running across invocations,
	// "ephemeral" starts a fresh one-shot container per command.
	Session string `yaml:"session,omitempty" json:"session,omitempty"`
}

// Load reads and parses a single flat YAML profile from disk. Multi-profile
// documents (a `profiles:` map) are handled by LoadDocument; Load is the flat
// path used by the named-profile store, where one file is one profile.
func Load(file string) (*Profile, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	p, err := decodeProfile(data)
	if err != nil {
		return nil, err
	}
	p.resolveImageNix(filepath.Dir(file))
	return p, nil
}

// decodeProfile strictly decodes one profile from YAML bytes (unknown fields are
// rejected, catching typos). Shared by Load and the flat path of LoadDocument.
func decodeProfile(data []byte) (*Profile, error) {
	var p Profile
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

// JSON serializes the profile to the camelCase JSON stored under _meta and
// mounted into the container.
func (p *Profile) JSON() ([]byte, error) {
	return json.MarshalIndent(p, "", "  ")
}

// EgressEnabled reports whether the container egress allowlist should be forced.
// Default true; an explicit `egress: false` in the profile disables it.
func (p *Profile) EgressEnabled() bool {
	return p.Egress == nil || *p.Egress
}

var sanitizeRe = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)
var dashTrimRe = regexp.MustCompile(`^-+|-+$`)

// Sanitize turns an arbitrary name into a safe slug (mirrors the bash
// sanitize(): collapse non [A-Za-z0-9_.-] runs to a single '-', trim leading
// and trailing dashes).
func Sanitize(s string) string {
	s = sanitizeRe.ReplaceAllString(s, "-")
	s = dashTrimRe.ReplaceAllString(s, "")
	return s
}
