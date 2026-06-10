package config

import (
	"bytes"
	"fmt"
	"os"
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

	// Probe (non-strict) for the multi-profile marker.
	var probe struct {
		Profiles map[string]yaml.Node `yaml:"profiles"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return nil, err
	}

	if probe.Profiles == nil {
		p, err := decodeProfile(data)
		if err != nil {
			return nil, err
		}
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
		return nil, err
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
	if over.Agent != "" {
		out.Agent = over.Agent
	}
	if over.Model != "" {
		out.Model = over.Model
	}
	if len(over.Network.AllowedDomains) > 0 {
		out.Network.AllowedDomains = over.Network.AllowedDomains
	}
	if over.Proxy.HTTP != "" {
		out.Proxy.HTTP = over.Proxy.HTTP
	}
	if over.Proxy.HTTPS != "" {
		out.Proxy.HTTPS = over.Proxy.HTTPS
	}
	if over.Proxy.No != "" {
		out.Proxy.No = over.Proxy.No
	}
	if len(over.Agents) > 0 {
		out.Agents = over.Agents
	}
	if over.Egress != nil {
		out.Egress = over.Egress
	}
	if len(over.Roots) > 0 {
		out.Roots = over.Roots
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
	out.Name = over.Name
	return out
}
