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
