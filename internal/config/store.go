package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ProfilesDir is the directory holding global named profiles. Resolution order:
//   - $SANDBOXER_PROFILES (explicit override);
//   - $XDG_CONFIG_HOME/sandboxer/profiles;
//   - ~/.config/sandboxer/profiles.
//
// It returns "" only when the home directory cannot be determined and no
// override is set.
func ProfilesDir() string {
	if d := os.Getenv("SANDBOXER_PROFILES"); d != "" {
		return d
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "sandboxer", "profiles")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "sandboxer", "profiles")
}

// ProfileRef is a discovered named profile: its effective name and file path.
type ProfileRef struct {
	Name string
	Path string
}

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

// yamlFiles lists the *.yaml/*.yml files directly under dir, sorted by path.
func yamlFiles(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if ext := filepath.Ext(e.Name()); ext == ".yaml" || ext == ".yml" {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out
}

// ListProfilesIn returns the named profiles found directly under dir. A
// profile's name is its file's base name unless the file sets an explicit
// name:. Files that fail to parse are skipped (so a stray YAML never breaks a
// listing).
func ListProfilesIn(dir string) []ProfileRef {
	var out []ProfileRef
	for _, f := range yamlFiles(dir) {
		p, err := Load(f)
		if err != nil {
			continue
		}
		out = append(out, ProfileRef{Name: ProfileName(f, p), Path: f})
	}
	return out
}

// ProfileSource names where a profile was discovered, in the precedence order
// resolveProfileFile consults them.
type ProfileSource string

const (
	SourceProject ProfileSource = "project" // .sandboxer/config.yaml
	SourceStore   ProfileSource = "store"   // ~/.config/sandboxer/profiles/*.yaml
)

// ProfileEntry is one profile discovered by ListAllProfiles: its name, the
// source it lives in and the file backing it, the effective backend it would run
// with, whether it is the project config's default:, and whether a
// higher-precedence source already defines the same name (so this entry is
// shadowed at resolution time).
type ProfileEntry struct {
	Name      string
	Source    ProfileSource
	Path      string
	Backend   string
	IsDefault bool
	Shadowed  bool
}

// ListAllProfiles enumerates every profile reachable by name across the two
// sources resolveProfileFile consults, in that precedence order: the project
// config, then the named-profile store. projectConfig is a file path and
// storeDir a directory; either being empty, absent, or unparseable simply
// contributes nothing (a stray file never breaks the listing). When the same
// name appears in both sources, the project one wins and the store entry is
// marked Shadowed — mirroring which profile create/enter/exec would actually
// pick.
func ListAllProfiles(projectConfig, storeDir string) []ProfileEntry {
	out := documentProfiles(projectConfig, SourceProject)
	for _, r := range ListProfilesIn(storeDir) {
		e := ProfileEntry{Name: r.Name, Source: SourceStore, Path: r.Path}
		if p, err := Load(r.Path); err == nil {
			e.Backend = p.Backend
		}
		out = append(out, e)
	}
	seen := make(map[string]bool, len(out))
	for i := range out {
		if seen[out[i].Name] {
			out[i].Shadowed = true
		} else {
			seen[out[i].Name] = true
		}
	}
	return out
}

// documentProfiles enumerates the named profiles in a single config Document
// (the project file), sorted by name. It returns nothing when path is empty,
// missing, or unparseable. IsDefault flags the document's default: for the
// project source.
func documentProfiles(path string, src ProfileSource) []ProfileEntry {
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
			Source:    src,
			Path:      path,
			IsDefault: src == SourceProject && name == d.Default,
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

// FindProfile returns the path of the profile named name under dir, matching by
// effective name (an explicit name: overrides the file's base name). It returns
// "" when nothing matches and an error when more than one file claims the name.
func FindProfile(dir, name string) (string, error) {
	if dir == "" || name == "" {
		return "", nil
	}
	var hits []string
	for _, r := range ListProfilesIn(dir) {
		if r.Name == name {
			hits = append(hits, r.Path)
		}
	}
	switch len(hits) {
	case 0:
		return "", nil
	case 1:
		return hits[0], nil
	default:
		return "", fmt.Errorf("profile %q is ambiguous in %s: %s", name, dir, strings.Join(hits, ", "))
	}
}
