package config

import (
	"path/filepath"
	"sort"
	"strings"
)

// ProfileName returns a profile's effective name: the explicit name: field when
// set, otherwise the file's base name without its extension. A nil profile
// yields the base-name form (used as the fallback slug for an unnamed file).
func ProfileName(path string, p *Profile) string {
	if p != nil && p.Name != "" {
		return p.Name
	}
	b := filepath.Base(path)
	return strings.TrimSuffix(b, filepath.Ext(b))
}

// ProfileEntry is one profile discovered by ListProfiles: its name, the file
// backing it, the backend it would run with, and whether it is the config's
// default:.
type ProfileEntry struct {
	Name      string
	Path      string
	Backend   string
	IsDefault bool
}

// ListProfiles enumerates the named profiles in one config file (flat = its
// single profile; multi = every profiles: section), sorted by name. It returns
// nothing when path is empty, missing, or unparseable — profiles live in ONE
// config file; there is no directory scan and no global store.
func ListProfiles(path string) []ProfileEntry {
	if path == "" {
		return nil
	}
	d, err := LoadDocument(path)
	if err != nil {
		return nil
	}
	out := make([]ProfileEntry, 0, len(d.Profiles))
	for name := range d.Profiles {
		e := ProfileEntry{
			Name:      name,
			Path:      path,
			IsDefault: name == d.Default,
		}
		if p, err := d.Select(name); err == nil {
			e.Backend = p.Backend
		} else {
			e.Backend = d.Profiles[name].Backend
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
