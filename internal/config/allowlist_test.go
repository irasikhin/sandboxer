package config

import (
	"encoding/json"
	"testing"
)

// The allowlist has three distinguishable inputs, and the one that reads most
// obviously as "deny everything" used to be the one that opened the whole
// built-in default set:
//
//	(absent)                → the defaults
//	allowedDomains = [ ... ] → exactly those
//	allowedDomains = [ ]     → nothing
//
// An absent attr decodes to a nil slice and an explicit empty list to a non-nil
// empty one, so the distinction survives the nix → JSON → Go trip; these pin
// that it survives ResolveRuntime too. This is a security boundary, not a
// convenience: silently substituting 40 domains for "none" fails OPEN.

func TestResolveRuntimeAllowlistPrecedence(t *testing.T) {
	const base = "base1.com,base2.com"
	empty := []string{}

	for _, tc := range []struct {
		name    string
		profile []string // nil = attr absent
		flag    string
		want    []string
	}{
		{"absent falls back to the defaults", nil, "", []string{"base1.com", "base2.com"}},
		{"explicit list replaces the defaults", []string{"a.com"}, "", []string{"a.com"}},
		{"explicit EMPTY list means deny-all", empty, "", nil},
		{"flag beats an absent profile attr", nil, "f.com", []string{"f.com"}},
		{"flag beats an explicit profile list", []string{"a.com"}, "f.com", []string{"f.com"}},
		{"flag beats an explicit empty list", empty, "f.com", []string{"f.com"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &Profile{}
			p.Egress.AllowedDomains = tc.profile
			rt, err := ResolveRuntime(p, Defaults{}, base, Overrides{Domains: tc.flag})
			if err != nil {
				t.Fatalf("ResolveRuntime: %v", err)
			}
			if len(rt.Domains) != len(tc.want) {
				t.Fatalf("domains = %v, want %v", rt.Domains, tc.want)
			}
			for i, d := range tc.want {
				if rt.Domains[i] != d {
					t.Errorf("domains[%d] = %q, want %q", i, rt.Domains[i], d)
				}
			}
		})
	}
}

// TestEmptyAllowlistSurvivesDecode is the other half of the contract: the
// nil-vs-empty distinction ResolveRuntime relies on has to still be there after
// the config document is decoded, or the fix above is unreachable from a real
// sandboxer.nix.
func TestEmptyAllowlistSurvivesDecode(t *testing.T) {
	var absent, explicit Profile
	if err := json.Unmarshal([]byte(`{"egress":{}}`), &absent); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"egress":{"allowedDomains":[]}}`), &explicit); err != nil {
		t.Fatal(err)
	}
	if absent.Egress.AllowedDomains != nil {
		t.Error("an absent allowedDomains should decode to nil (meaning: use the defaults)")
	}
	if explicit.Egress.AllowedDomains == nil {
		t.Error("an explicit [] decoded to nil — deny-all would silently become the default allowlist")
	}
	if len(explicit.Egress.AllowedDomains) != 0 {
		t.Errorf("explicit [] decoded to %v", explicit.Egress.AllowedDomains)
	}
}
