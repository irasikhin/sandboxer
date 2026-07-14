package config

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Key is one addressable profile key for config get/set/unset: its dotted
// path and whether its terminal Go type is a string. IsString drives value
// typing in ParseValue: string fields take the raw argument verbatim (so
// `8080`, a URL or a multiline script is never re-typed by YAML), everything
// else is parsed as YAML.
type Key struct {
	Path     string
	IsString bool
}

// envPrefix is the dotted prefix addressing one env var (env.<NAME>).
const envPrefix = "env."

// ProfileKeys returns every addressable profile key, sorted by path. It is
// reflected from Profile's yaml tags — the same tags the decoder and the JSON
// schema use — so the registry cannot drift from the struct.
func ProfileKeys() []Key {
	var keys []Key
	collectKeys(reflect.TypeOf(Profile{}), "", &keys)
	sort.Slice(keys, func(i, j int) bool { return keys[i].Path < keys[j].Path })
	return keys
}

func collectKeys(t reflect.Type, prefix string, keys *[]Key) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if tag == "" || tag == "-" {
			continue
		}
		path := tag
		if prefix != "" {
			path = prefix + "." + tag
		}
		if f.Type.Kind() == reflect.Struct {
			collectKeys(f.Type, path, keys)
			continue
		}
		*keys = append(*keys, Key{Path: path, IsString: f.Type.Kind() == reflect.String})
	}
}

// LookupKey resolves a dotted user key to its registry entry. env.<NAME>
// addresses one env var (NAME itself must not contain a dot). A removed key
// gets its migration hint; an unknown key gets a did-you-mean suggestion or
// the full key list.
func LookupKey(path string) (Key, error) {
	if strings.HasPrefix(path, envPrefix) {
		name := strings.TrimPrefix(path, envPrefix)
		if name == "" || strings.Contains(name, ".") {
			return Key{}, fmt.Errorf("invalid env key %q — use env.<NAME> with a dot-free name", path)
		}
		return Key{Path: path, IsString: true}, nil
	}
	keys := ProfileKeys()
	for _, k := range keys {
		if k.Path == path {
			return k, nil
		}
	}
	if hint, ok := removedKeys[path]; ok {
		return Key{}, fmt.Errorf("unknown key %q — `%s:` %s", path, path, hint)
	}
	if s := closestKey(path, keys); s != "" {
		return Key{}, fmt.Errorf("unknown key %q — did you mean %q?", path, s)
	}
	all := make([]string, len(keys))
	for i, k := range keys {
		all[i] = k.Path
	}
	return Key{}, fmt.Errorf("unknown key %q — known keys: %s, env.<NAME>", path, strings.Join(all, ", "))
}

// ParseValue turns a raw CLI value into a yaml node for EditableConfig.Set.
// String-typed keys take the value verbatim; other keys parse as YAML, so
// `false`, `512`, `[a, b]` and `[{source: /x, target: /y}]` all work. Type
// correctness of the result is enforced by the caller's strict re-decode of
// the whole edited document.
func ParseValue(raw string, k Key) (*yaml.Node, error) {
	if k.IsString {
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: raw}, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		return nil, fmt.Errorf("invalid value %q: %v", raw, err)
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return nil, fmt.Errorf("empty value for %s — to remove a key use 'config unset'", k.Path)
	}
	return doc.Content[0], nil
}

// ProfileValue reads one dotted key's value from a (merged) profile via the
// same yaml tags the registry is built from. The second result is false when
// the key is unset — a zero value, a nil egress, or an absent env var.
func ProfileValue(p *Profile, path string) (any, bool) {
	if strings.HasPrefix(path, envPrefix) {
		v, ok := p.Env[strings.TrimPrefix(path, envPrefix)]
		return v, ok
	}
	v := reflect.ValueOf(*p)
	for _, seg := range strings.Split(path, ".") {
		if v.Kind() != reflect.Struct {
			return nil, false
		}
		f, ok := fieldByYAMLTag(v.Type(), seg)
		if !ok {
			return nil, false
		}
		v = v.FieldByIndex(f.Index)
	}
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil, false
		}
		// A non-nil pointer is explicitly set even when it points at a zero
		// value — `egress: false` must read as set.
		return v.Elem().Interface(), true
	}
	if v.IsZero() {
		return nil, false
	}
	return v.Interface(), true
}

func fieldByYAMLTag(t reflect.Type, tag string) (reflect.StructField, bool) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == tag {
			return f, true
		}
	}
	return reflect.StructField{}, false
}

// closestKey returns the known key within edit distance 2 of path (the
// nearest one), or "" when nothing is close enough to suggest.
func closestKey(path string, keys []Key) string {
	best, bestD := "", 3
	for _, k := range keys {
		if d := editDistance(path, k.Path); d < bestD {
			best, bestD = k.Path, d
		}
	}
	return best
}

// editDistance is the Levenshtein distance between two short strings.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, min(cur[j-1]+1, prev[j-1]+cost))
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
