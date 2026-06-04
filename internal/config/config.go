// Package config models the sandboxer profile and resolves runtime settings.
//
// A profile is optional: scalar settings come from flags and SANDBOXER_* env
// vars, and only structured fields (roots/deps vendoring, extraMounts, env)
// require a sandboxer.yaml file. The resolved profile is serialized to JSON
// (camelCase keys) and stored under .sandboxer/_meta/<slug>.profile.json; that
// JSON is the single artifact the container backend and the srcs package read.
package config

import (
	"encoding/json"
	"os"
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

// Proxy is an upstream corporate proxy passed through to the sandbox.
type Proxy struct {
	HTTP  string `yaml:"http,omitempty"  json:"http,omitempty"`
	HTTPS string `yaml:"https,omitempty" json:"https,omitempty"`
	No    string `yaml:"no,omitempty"    json:"no,omitempty"`
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
}

// Load reads and parses a single flat YAML profile from disk. Multi-profile
// documents (a `profiles:` map) are handled by LoadDocument; Load is the flat
// path used by the named-profile store, where one file is one profile.
func Load(file string) (*Profile, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	return decodeProfile(data)
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
