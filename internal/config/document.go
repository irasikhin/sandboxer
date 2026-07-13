package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Document is a profile file in either of two shapes:
//
//   - flat: the Profile fields at the top level (one profile per file), the
//     original form; or
//   - multi: a `profiles:` map of named profiles, with an optional shared
//     `defaults:` base merged into each and an optional `default:` naming the
//     profile used when none is requested.
//
// LoadDocument normalizes both into a Document so callers can Select a profile
// by name without caring which shape the file uses.
type Document struct {
	Defaults Profile            `yaml:"defaults,omitempty"`
	Profiles map[string]Profile `yaml:"profiles,omitempty"`
	Default  string             `yaml:"default,omitempty"`

	multi bool // the file used the `profiles:` form
}

// LoadDocument reads a profile file and normalizes it. A file with a top-level
// `profiles:` key is parsed as the multi form (strict); anything else is parsed
// as a single flat profile (strict) and wrapped as a one-entry document keyed by
// its effective name.
func LoadDocument(file string) (*Document, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}

	// Probe (non-strict) for the document markers. Either a `profiles:` map or a
	// `defaults:` block puts the file in the multi/document form — the latter so a
	// defaults-only file (the common shape of the global config, which contributes
	// a base under the project with no profiles of its own) parses as a Document
	// rather than failing strict parse as a flat Profile.
	var probe struct {
		Profiles map[string]yaml.Node `yaml:"profiles"`
		Defaults yaml.Node            `yaml:"defaults"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return nil, err
	}

	dir := filepath.Dir(file)
	if probe.Profiles == nil && probe.Defaults.IsZero() {
		p, err := decodeProfile(data)
		if err != nil {
			return nil, err
		}
		p.resolveImageNix(dir)
		name := ProfileName(file, p)
		p.Name = name
		return &Document{
			Profiles: map[string]Profile{name: *p},
			Default:  name,
		}, nil
	}

	var d Document
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&d); err != nil {
		return nil, annotateRemovedKeys(err)
	}
	// Resolve relative image.nix paths against the file's directory in every
	// section — defaults: included — so the snapshot written to _meta is
	// self-contained.
	d.Defaults.resolveImageNix(dir)
	for name, p := range d.Profiles {
		p.resolveImageNix(dir)
		d.Profiles[name] = p
	}
	d.multi = true
	return &d, nil
}

// Multi reports whether the document used the `profiles:` form. Callers use it to
// decide whether a positional argument selects a section or is a slug override.
func (d *Document) Multi() bool { return d.multi }

// Has reports whether the document defines a profile named name.
func (d *Document) Has(name string) bool {
	_, ok := d.Profiles[name]
	return ok
}

// Select resolves one profile by name, layering it over the shared defaults.
// Inheritance between profiles is expressed with native YAML anchors and merge
// keys (`&base` / `<<: *base`), resolved by the YAML decoder before we get here,
// so no extra mechanism is needed. An empty name uses `default:` (or the sole
// profile when there is exactly one). The returned profile's Name is set to the
// selected key.
func (d *Document) Select(name string) (*Profile, error) {
	if name == "" {
		name = d.Default
	}
	if name == "" {
		if len(d.Profiles) == 1 {
			for k := range d.Profiles {
				name = k
			}
		} else {
			return nil, fmt.Errorf("name a profile (have: %s)", d.names())
		}
	}
	sec, ok := d.Profiles[name]
	if !ok {
		return nil, fmt.Errorf("no profile %q (have: %s)", name, d.names())
	}
	eff := mergeProfile(d.Defaults, sec)
	eff.Name = name
	return &eff, nil
}

// SelectWithGlobal resolves a profile the same way Select does, but layers the
// project document OVER an optional global document so the project always wins.
//
// The effective base for the selected section is mergeProfile(global.Defaults,
// project.Defaults) — global defaults sit UNDER the project defaults — and the
// selected section merges on top of that. A nil global makes this exactly
// Select(name).
//
// Named-profile resolution falls back project -> global: a name found in this
// (project) document selects its section; a name absent here but present in the
// global document's profiles: map selects the global section (still over the
// composed defaults, so a project default still overrides a field the global
// profile inherited). An empty name uses this document's default: (or its sole
// profile), never the global's — the global layer is a base, not the thing
// being selected.
func (d *Document) SelectWithGlobal(name string, global *Document) (*Profile, error) {
	if global == nil {
		return d.Select(name)
	}
	base := mergeProfile(global.Defaults, d.Defaults)

	if name == "" {
		name = d.Default
	}
	if name == "" {
		if len(d.Profiles) == 1 {
			for k := range d.Profiles {
				name = k
			}
		} else {
			return nil, fmt.Errorf("name a profile (have: %s)", d.names())
		}
	}

	sec, ok := d.Profiles[name]
	if !ok {
		// Fall back to a same-named profile in the global document.
		if g, gok := global.Profiles[name]; gok {
			sec = g
		} else {
			return nil, fmt.Errorf("no profile %q (have: %s)", name, d.names())
		}
	}

	eff := mergeProfile(base, sec)
	eff.Name = name
	return &eff, nil
}

// names renders the sorted profile names for an error message.
func (d *Document) names() string {
	out := make([]string, 0, len(d.Profiles))
	for k := range d.Profiles {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// FileHasProfile reports whether file defines a profile named name, in either
// the flat (the single profile's effective name) or multi (a `profiles:`
// section) form. It is used during resolution to decide whether a bare
// positional selects a profile from the project file. Parse errors yield false.
func FileHasProfile(file, name string) bool {
	d, err := LoadDocument(file)
	return err == nil && d.Has(name)
}

// mergeProfile overlays over onto base field by field: a field set on over wins,
// otherwise base's value is kept. Env maps merge key-wise (base entries plus
// over entries, over winning) so a profile can extend the shared env rather than
// replace it; every other field is a whole-value override.
func mergeProfile(base, over Profile) Profile {
	out := base
	if over.Backend != "" {
		out.Backend = over.Backend
	}
	// Network merges per field: a profile can tighten the allowlist while keeping
	// the defaults' proxy, or set a proxy without re-listing domains.
	if len(over.Network.AllowedDomains) > 0 {
		out.Network.AllowedDomains = over.Network.AllowedDomains
	}
	if over.Network.Proxy != "" {
		out.Network.Proxy = over.Network.Proxy
	}
	if over.Network.NoProxy != "" {
		out.Network.NoProxy = over.Network.NoProxy
	}
	if len(over.Network.Routes) > 0 {
		out.Network.Routes = over.Network.Routes
	}
	if len(over.Agents) > 0 {
		out.Agents = over.Agents
	}
	if over.Egress != nil {
		out.Egress = over.Egress
	}
	if len(over.Deps) > 0 {
		out.Deps = over.Deps
	}
	if len(over.ExtraMounts) > 0 {
		out.ExtraMounts = over.ExtraMounts
	}
	if len(over.Env) > 0 {
		m := make(map[string]string, len(base.Env)+len(over.Env))
		for k, v := range base.Env {
			m[k] = v
		}
		for k, v := range over.Env {
			m[k] = v
		}
		out.Env = m
	}
	if over.Setup != "" {
		out.Setup = over.Setup
	}
	if len(over.Tools) > 0 {
		out.Tools = over.Tools
	}
	// Image merges per field, so defaults: can pin revisions while a profile
	// adds packages or a user nix file on top.
	if len(over.Image.ExtraPkgs) > 0 {
		out.Image.ExtraPkgs = over.Image.ExtraPkgs
	}
	if over.Image.Nix != "" {
		out.Image.Nix = over.Image.Nix
	}
	if over.Image.LLMAgentsRev != "" {
		out.Image.LLMAgentsRev = over.Image.LLMAgentsRev
	}
	if over.Image.NixpkgsRev != "" {
		out.Image.NixpkgsRev = over.Image.NixpkgsRev
	}
	if len(over.MCP) > 0 {
		out.MCP = over.MCP
	}
	if over.Session != "" {
		out.Session = over.Session
	}
	// Limits merge per field, so defaults: can cap memory while a profile tightens
	// cpus or pids on top.
	if over.Limits.Memory != "" {
		out.Limits.Memory = over.Limits.Memory
	}
	if over.Limits.CPUs != "" {
		out.Limits.CPUs = over.Limits.CPUs
	}
	if over.Limits.Pids != 0 {
		out.Limits.Pids = over.Limits.Pids
	}
	out.Name = over.Name
	return out
}
