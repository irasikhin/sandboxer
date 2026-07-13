package config

import (
	"reflect"
	"testing"
)

// TestMergeProfileOverrides pins the field-by-field override semantics of
// mergeProfile: every field set on `over` wins, while a field left empty on
// `over` keeps the base value.
func TestMergeProfileOverrides(t *testing.T) {
	// given: a base with a couple of fields, and an over that sets every
	// otherwise-untested override field.
	base := Profile{
		Backend: "podman",
		Agent:   "claude",
		Network: Network{AllowedDomains: []string{"base.example"}},
	}
	egress := true
	over := Profile{
		Network:     Network{AllowedDomains: []string{"over.example"}, Proxy: "http://p:1", NoProxy: "localhost"},
		Agents:      []string{"codex"},
		Egress:      &egress,
		ExtraMounts: []Mount{{}},
		Setup:       "npm ci",
		Tools:       []string{"node"},
		MCP:         []string{"fetch"},
	}

	// when
	got := mergeProfile(base, over)

	// then: each override wins
	if len(got.Network.AllowedDomains) != 1 || got.Network.AllowedDomains[0] != "over.example" {
		t.Errorf("AllowedDomains = %v, want [over.example]", got.Network.AllowedDomains)
	}
	if got.Network.Proxy != over.Network.Proxy {
		t.Errorf("Network.Proxy = %q, want %q", got.Network.Proxy, over.Network.Proxy)
	}
	if got.Network.NoProxy != over.Network.NoProxy {
		t.Errorf("Network.NoProxy = %q, want %q", got.Network.NoProxy, over.Network.NoProxy)
	}
	if len(got.Agents) != 1 || got.Agents[0] != "codex" {
		t.Errorf("Agents = %v, want [codex]", got.Agents)
	}
	if got.Egress == nil || !*got.Egress {
		t.Errorf("Egress = %v, want overridden true", got.Egress)
	}
	if len(got.ExtraMounts) != 1 {
		t.Errorf("ExtraMounts len = %d, want 1", len(got.ExtraMounts))
	}
	// Setup/Tools/MCP must survive the merge — Select runs EVERY profile load
	// (flat files included) through it, so a dropped field here silently
	// disables the whole feature downstream (no setup run, no tool-pack image,
	// no MCP wiring).
	if got.Setup != "npm ci" {
		t.Errorf("Setup = %q, want npm ci", got.Setup)
	}
	if len(got.Tools) != 1 || got.Tools[0] != "node" {
		t.Errorf("Tools = %v, want [node]", got.Tools)
	}
	if len(got.MCP) != 1 || got.MCP[0] != "fetch" {
		t.Errorf("MCP = %v, want [fetch]", got.MCP)
	}
	// and: fields left empty on over keep the base value
	if got.Backend != "podman" || got.Agent != "claude" {
		t.Errorf("base fields lost: backend=%q agent=%q", got.Backend, got.Agent)
	}
}

// populateValue fills v with a non-zero value derived from path, recursing
// into structs/slices/maps/pointers. It fails the test on a kind it does not
// know how to build, so a new Profile field of a new shape cannot be silently
// skipped by TestMergeProfileExhaustive.
func populateValue(t *testing.T, v reflect.Value, path string) {
	t.Helper()
	switch v.Kind() {
	case reflect.String:
		v.SetString("val-" + path)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		populateValue(t, v.Elem(), path)
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		populateValue(t, v.Index(0), path+"[0]")
	case reflect.Map:
		key := reflect.New(v.Type().Key()).Elem()
		populateValue(t, key, path+".key")
		val := reflect.New(v.Type().Elem()).Elem()
		populateValue(t, val, path+".val")
		v.Set(reflect.MakeMap(v.Type()))
		v.SetMapIndex(key, val)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if !f.IsExported() {
				continue
			}
			populateValue(t, v.Field(i), path+"."+f.Name)
		}
	default:
		t.Fatalf("populateValue: unsupported kind %s at %s — teach the builder so the merge guard stays exhaustive", v.Kind(), path)
	}
}

// TestMergeProfileExhaustive is the merge guard for the whole Profile shape:
// every exported field (recursively) is set non-zero via reflection, and the
// merge over an empty base must reproduce it exactly. mergeProfile is a
// hand-enumerated field list, and this class of bug has recurred (Session,
// then Setup/Tools/MCP/Proxy/NoProxy were silently dropped) — with this
// test, adding a Profile field without a merge case fails CI instead of
// silently disabling the feature downstream.
func TestMergeProfileExhaustive(t *testing.T) {
	var over Profile
	populateValue(t, reflect.ValueOf(&over).Elem(), "Profile")

	// Empty base: every populated over field must survive the merge intact.
	got := mergeProfile(Profile{}, over)
	if !reflect.DeepEqual(got, over) {
		t.Errorf("mergeProfile(empty, full) dropped or rewrote fields:\ngot  %+v\nwant %+v", got, over)
	}

	// Full base, empty over: everything is kept — except Name, which is
	// always taken from over (Select re-stamps it afterward).
	kept := mergeProfile(over, Profile{})
	want := over
	want.Name = ""
	if !reflect.DeepEqual(kept, want) {
		t.Errorf("mergeProfile(full, empty) lost base fields:\ngot  %+v\nwant %+v", kept, want)
	}
}

// TestMergeProfileContext: context follows the standard list-override rule —
// a non-empty over replaces, an empty over keeps the base.
func TestMergeProfileContext(t *testing.T) {
	base := Profile{Context: []string{"CLAUDE.md"}}
	got := mergeProfile(base, Profile{Context: []string{"NOTES.md"}})
	if !reflect.DeepEqual(got.Context, []string{"NOTES.md"}) {
		t.Errorf("over.Context must win: %v", got.Context)
	}
	got = mergeProfile(base, Profile{})
	if !reflect.DeepEqual(got.Context, []string{"CLAUDE.md"}) {
		t.Errorf("empty over must keep base.Context: %v", got.Context)
	}
}
