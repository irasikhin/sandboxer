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
//   - multi: a `profiles:` map of named, SELF-CONTAINED profiles, with an
//     optional `default:` naming the profile used when none is requested.
//
// There is no config-level inheritance: no shared defaults block and no
// merging between files — a profile is exactly what its section says. Reuse
// between sections, when wanted, is plain YAML (anchors + `<<:` merge keys),
// resolved by the decoder before we ever see it.
//
// LoadDocument normalizes both shapes into a Document so callers can Select a
// profile by name without caring which one the file uses.
type Document struct {
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
	return LoadDocumentBytes(data, file)
}

// LoadDocumentBytes parses profile config bytes exactly as LoadDocument does,
// without touching the filesystem. file is the nominal path the bytes belong
// to: its directory anchors relative image.nix resolution and its base name is
// the flat-form name fallback. It exists so an in-memory edit (config set) can
// be strictly validated before anything lands on disk.
func LoadDocumentBytes(data []byte, file string) (*Document, error) {
	// Probe (non-strict) for the document marker: a `profiles:` map puts the
	// file in the multi/document form; anything else is a flat profile.
	var probe struct {
		Profiles map[string]yaml.Node `yaml:"profiles"`
	}
	if err := yaml.Unmarshal(data, &probe); err != nil {
		return nil, err
	}

	dir := filepath.Dir(file)
	if probe.Profiles == nil {
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
	// section, so the snapshot written to _meta is self-contained. (srcs paths
	// deliberately stay relative: they resolve against the PROJECT ROOT at
	// sandbox-sync time, so a store/-f profile's "src: ." means the project.)
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

// Select resolves one profile by name. Sections are self-contained — there is
// no defaults layer; reuse between profiles is plain YAML anchors and merge
// keys (`&base` / `<<: *base`), resolved by the decoder before we get here.
// An empty name uses `default:` (or the sole profile when there is exactly
// one). The returned profile's Name is set to the selected key.
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
// the flat (the single profile's effective name) or multi (a `profiles:`
// section) form. It is used during resolution to decide whether a bare
// positional selects a profile from the project file. Parse errors yield false.
func FileHasProfile(file, name string) bool {
	d, err := LoadDocument(file)
	return err == nil && d.Has(name)
}
