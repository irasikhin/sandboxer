package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

// EditableConfig is a profile config file parsed to a yaml.Node tree for
// surgical, comment-preserving edits (config set/unset). Re-marshaling the
// decoded structs would drop every comment and reorder keys; node surgery
// touches only the addressed key, so the user's annotations — including the
// heavily-commented scaffold — survive. Profile targeting (profiles.<name> vs
// the flat top level) is the caller's job: methods take full node paths.
type EditableConfig struct {
	root  *yaml.Node // the DocumentNode; Content[0] is the top-level mapping
	multi bool       // the file uses the profiles:/defaults: document form
}

// ParseEditable parses config bytes into an editable node tree. An empty (or
// comment-only) file becomes an empty mapping document; a multi-document
// stream is rejected.
func ParseEditable(data []byte) (*EditableConfig, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	root := &yaml.Node{}
	switch err := dec.Decode(root); {
	case errors.Is(err, io.EOF):
		root = &yaml.Node{
			Kind:    yaml.DocumentNode,
			Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}},
		}
	case err != nil:
		return nil, err
	default:
		var extra yaml.Node
		if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
			return nil, errors.New("multiple yaml documents in one file are not supported")
		}
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 {
		return nil, errors.New("unsupported yaml document shape")
	}
	top := root.Content[0]
	if isNullNode(top) {
		// A bare `---` document: fill it in place (keeps document comments).
		makeMapping(top)
	}
	if top.Kind != yaml.MappingNode {
		return nil, errors.New("top level is not a yaml mapping")
	}
	multi := findKey(top, "profiles") >= 0 || findKey(top, "defaults") >= 0
	return &EditableConfig{root: root, multi: multi}, nil
}

// Multi reports whether the file uses the profiles:/defaults: document form —
// the same probe LoadDocument applies — so callers know whether a profile key
// lives under profiles.<name> or at the top level.
func (e *EditableConfig) Multi() bool { return e.multi }

// Set writes value at the dotted node path, creating intermediate mappings as
// needed. Replacing an existing value keeps the key node (and its comments)
// untouched and carries the old value's comments and anchor over to the new
// node, so `&name` definitions other sections alias stay alive.
func (e *EditableConfig) Set(path []string, value *yaml.Node) error {
	if len(path) == 0 {
		return errors.New("empty key path")
	}
	m := e.root.Content[0]
	for i, seg := range path[:len(path)-1] {
		next, err := descend(m, seg, true, strings.Join(path[:i+1], "."))
		if err != nil {
			return err
		}
		m = next
	}
	last := path[len(path)-1]
	if idx := findKey(m, last); idx >= 0 {
		old := m.Content[idx+1]
		value.HeadComment = old.HeadComment
		value.LineComment = old.LineComment
		value.FootComment = old.FootComment
		value.Anchor = old.Anchor
		m.Content[idx+1] = value
		return nil
	}
	m.Content = append(m.Content, keyNode(last), value)
	return nil
}

// Unset removes the key at the dotted node path. It reports false when the
// key is not present in the raw tree — including a value only inherited via a
// YAML merge key (`<<:`) or a defaults: layer, which lives elsewhere. An
// emptied parent mapping is left in place (pruning could delete the user's
// section comments).
func (e *EditableConfig) Unset(path []string) (bool, error) {
	if len(path) == 0 {
		return false, errors.New("empty key path")
	}
	m := e.root.Content[0]
	for i, seg := range path[:len(path)-1] {
		next, err := descend(m, seg, false, strings.Join(path[:i+1], "."))
		if err != nil {
			return false, err
		}
		if next == nil {
			return false, nil
		}
		m = next
	}
	idx := findKey(m, path[len(path)-1])
	if idx < 0 {
		return false, nil
	}
	m.Content = append(m.Content[:idx], m.Content[idx+2:]...)
	return true, nil
}

// Bytes re-encodes the tree (2-space indent, trailing newline). The output is
// not byte-identical to the input — the encoder normalizes indentation and
// re-wraps long lines — but comments, key order, anchors and aliases survive.
func (e *EditableConfig) Bytes() ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(e.root); err != nil {
		_ = enc.Close()
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// descend resolves one path segment inside mapping m, optionally creating a
// missing (or null) intermediate mapping. at is the dotted path up to and
// including seg, for error messages. A nil, nil return means "absent" (only
// when create is false). Descending through an alias is refused: mutating the
// target would silently edit the anchored section other profiles share.
func descend(m *yaml.Node, seg string, create bool, at string) (*yaml.Node, error) {
	if idx := findKey(m, seg); idx >= 0 {
		v := m.Content[idx+1]
		switch {
		case v.Kind == yaml.AliasNode:
			return nil, fmt.Errorf("%s is an alias (*%s) — edit the anchored section instead, or replace the alias with explicit keys", at, v.Value)
		case isNullNode(v):
			if !create {
				return nil, nil
			}
			makeMapping(v) // an empty section (`web:`): fill it, keeping comments
		case v.Kind != yaml.MappingNode:
			return nil, fmt.Errorf("%s is not a mapping", at)
		}
		return v, nil
	}
	if !create {
		return nil, nil
	}
	v := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	m.Content = append(m.Content, keyNode(seg), v)
	return v, nil
}

// findKey returns the index of key's key-node in mapping m's Content pairs,
// or -1 when absent.
func findKey(m *yaml.Node, key string) int {
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := m.Content[i]
		if k.Kind == yaml.ScalarNode && k.Value == key {
			return i
		}
	}
	return -1
}

func keyNode(key string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
}

func isNullNode(n *yaml.Node) bool {
	return n.Kind == yaml.ScalarNode && n.Tag == "!!null"
}

// makeMapping converts n into an empty mapping in place, preserving its
// comment and anchor fields.
func makeMapping(n *yaml.Node) {
	n.Kind = yaml.MappingNode
	n.Tag = "!!map"
	n.Value = ""
	n.Content = nil
}
