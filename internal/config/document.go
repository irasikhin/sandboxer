package config

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Document is an evaluated config in either of two shapes:
//
//   - flat: the profile attrs at the top level (one profile per file); or
//   - multi: { profiles = { <name> = {...}; }; default = "<name>"; } — a map
//     of named, SELF-CONTAINED profiles with an optional default.
//
// There is no config-level inheritance: no shared defaults block and no
// merging between files — a profile is exactly what its attrset says. Reuse
// between sections is ordinary nix (let bindings, functions, // merges),
// resolved by the evaluator before we ever see the JSON.
//
// LoadDocument normalizes both shapes into a Document so callers can Select a
// profile by name without caring which one the file uses.
type Document struct {
	Profiles map[string]Profile
	Default  string

	multi bool // the config used the profiles = {...} form
}

// LoadDocument evaluates a sandboxer.nix config (EvalConfig — host nix,
// restricted eval) and normalizes it. An attrset with a top-level `profiles`
// key is the multi form; anything else is a single flat profile wrapped as a
// one-entry document keyed by its effective name.
func LoadDocument(file string) (*Document, error) {
	data, err := EvalConfig(file)
	if err != nil {
		return nil, err
	}
	return parseDocument(data, file)
}

// parseDocument decodes the evaluated JSON strictly (unknown keys are errors,
// with removed-key migration hints) and resolves relative image.nix paths
// against the config's directory so the _meta snapshot stays self-contained.
func parseDocument(data []byte, file string) (*Document, error) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("%s: the config must evaluate to an attrset: %w", file, err)
	}

	dir := filepath.Dir(file)
	if _, multi := probe["profiles"]; !multi {
		p, err := decodeProfileJSON(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		p.resolveImageNix(dir)
		name := ProfileName(file, p)
		p.Name = name
		return &Document{
			Profiles: map[string]Profile{name: *p},
			Default:  name,
		}, nil
	}

	d := &Document{Profiles: map[string]Profile{}, multi: true}
	for key, raw := range probe {
		switch key {
		case "profiles":
			var sections map[string]json.RawMessage
			if err := json.Unmarshal(raw, &sections); err != nil {
				return nil, fmt.Errorf("%s: profiles must be an attrset of profiles: %w", file, err)
			}
			for name, sec := range sections {
				p, err := decodeProfileJSON(sec)
				if err != nil {
					return nil, fmt.Errorf("%s: profiles.%s: %w", file, name, err)
				}
				p.resolveImageNix(dir)
				d.Profiles[name] = *p
			}
		case "default":
			if err := json.Unmarshal(raw, &d.Default); err != nil {
				return nil, fmt.Errorf("%s: default must be a profile name string: %w", file, err)
			}
		default:
			if hint, ok := removedKeys[key]; ok {
				return nil, fmt.Errorf("%s: unknown top-level key %q — %s", file, key, hint)
			}
			return nil, fmt.Errorf("%s: unknown top-level key %q — the multi form allows only profiles and default", file, key)
		}
	}
	return d, nil
}

// Multi reports whether the document used the profiles = {...} form. Callers
// use it to decide whether a positional argument selects a section or is a
// slug override.
func (d *Document) Multi() bool { return d.multi }

// Has reports whether the document defines a profile named name.
func (d *Document) Has(name string) bool {
	_, ok := d.Profiles[name]
	return ok
}

// Select resolves one profile by name. Sections are self-contained — there is
// no defaults layer; reuse between profiles is ordinary nix, resolved by the
// evaluator before we get here. An empty name uses `default` (or the sole
// profile when there is exactly one). The returned profile's Name is set to
// the selected key.
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
	sec.Name = name
	return &sec, nil
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
// the flat (the single profile's effective name) or multi (a profiles
// section) form. It is used during resolution to decide whether a bare
// positional selects a profile from the project file. Eval/parse errors yield
// false.
func FileHasProfile(file, name string) bool {
	d, err := LoadDocument(file)
	return err == nil && d.Has(name)
}
