package config

import (
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestProfileKeys pins the full addressable key set. A new Profile field must
// show up here (with the right string-ness) — the registry is reflected, so
// this is the drift alarm, not a second source of truth.
func TestProfileKeys(t *testing.T) {
	want := []Key{
		{Path: "agents", IsString: false},
		{Path: "backend", IsString: true},
		{Path: "egress", IsString: false},
		{Path: "env", IsString: false},
		{Path: "extraMounts", IsString: false},
		{Path: "image.extraPkgs", IsString: false},
		{Path: "image.llmAgentsRev", IsString: true},
		{Path: "image.nix", IsString: true},
		{Path: "image.nixpkgsRev", IsString: true},
		{Path: "limits.cpus", IsString: true},
		{Path: "limits.memory", IsString: true},
		{Path: "limits.pids", IsString: false},
		{Path: "name", IsString: true},
		{Path: "network.allowedDomains", IsString: false},
		{Path: "network.noProxy", IsString: true},
		{Path: "network.proxy", IsString: true},
		{Path: "network.routes", IsString: false},
		{Path: "session", IsString: true},
		{Path: "setup", IsString: true},
		{Path: "srcs", IsString: false},
		{Path: "tools", IsString: false},
	}
	got := ProfileKeys()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ProfileKeys() =\n%v\nwant\n%v", got, want)
	}
}

func TestLookupKey(t *testing.T) {
	for _, tt := range []struct {
		path    string
		ok      bool
		errPart string
	}{
		{path: "backend", ok: true},
		{path: "network.proxy", ok: true},
		{path: "env.NODE_ENV", ok: true},
		{path: "env.", errPart: "env.<NAME>"},
		{path: "env.A.B", errPart: "dot-free"},
		{path: "proxy", errPart: "network.proxy"},                          // removed-key migration hint
		{path: "roots", errPart: "git worktrees"},                          // removed-key migration hint
		{path: "network.alowedDomains", errPart: "network.allowedDomains"}, // did-you-mean
		{path: "sesion", errPart: `"session"`},                             // did-you-mean
		{path: "zzzzzz", errPart: "known keys:"},                           // nothing close
	} {
		k, err := LookupKey(tt.path)
		if tt.ok {
			if err != nil {
				t.Errorf("LookupKey(%q) = %v", tt.path, err)
			} else if k.Path != tt.path {
				t.Errorf("LookupKey(%q).Path = %q", tt.path, k.Path)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tt.errPart) {
			t.Errorf("LookupKey(%q) = %v, want error containing %q", tt.path, err, tt.errPart)
		}
	}
}

// TestLookupKeyEnvIsString: env values are strings, so a numeric-looking value
// must be typed !!str (8080 into a string field fails the strict decode
// otherwise).
func TestParseValue(t *testing.T) {
	str := func(p string) Key { k, _ := LookupKey(p); return k }
	for _, tt := range []struct {
		key     Key
		raw     string
		tag     string
		kind    yaml.Kind
		errPart string
	}{
		{key: str("env.PORT"), raw: "8080", tag: "!!str", kind: yaml.ScalarNode},
		{key: str("limits.cpus"), raw: "2", tag: "!!str", kind: yaml.ScalarNode},
		{key: str("setup"), raw: "npm ci\nnpm run build", tag: "!!str", kind: yaml.ScalarNode},
		{key: str("egress"), raw: "false", tag: "!!bool", kind: yaml.ScalarNode},
		{key: str("limits.pids"), raw: "512", tag: "!!int", kind: yaml.ScalarNode},
		{key: str("srcs"), raw: "[{src: ., include: [\"/lib/\"]}]", kind: yaml.SequenceNode},
		{key: str("extraMounts"), raw: "[{source: /x, target: /y, mode: rw}]", kind: yaml.SequenceNode},
		{key: str("srcs"), raw: "[unclosed", errPart: "invalid value"},
		{key: str("egress"), raw: "", errPart: "empty value"},
	} {
		n, err := ParseValue(tt.raw, tt.key)
		if tt.errPart != "" {
			if err == nil || !strings.Contains(err.Error(), tt.errPart) {
				t.Errorf("ParseValue(%q, %s) = %v, want %q", tt.raw, tt.key.Path, err, tt.errPart)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseValue(%q, %s): %v", tt.raw, tt.key.Path, err)
			continue
		}
		if n.Kind != tt.kind {
			t.Errorf("ParseValue(%q, %s).Kind = %v, want %v", tt.raw, tt.key.Path, n.Kind, tt.kind)
		}
		if tt.tag != "" && n.Tag != tt.tag {
			t.Errorf("ParseValue(%q, %s).Tag = %q, want %q", tt.raw, tt.key.Path, n.Tag, tt.tag)
		}
	}
}

func TestProfileValue(t *testing.T) {
	on := true
	p := &Profile{
		Backend: "podman",
		Network: Network{Proxy: "http://p:1", AllowedDomains: []string{"a.com"}},
		Egress:  &on,
		Env:     map[string]string{"FOO": "bar"},
		Limits:  Limits{Pids: 512},
	}
	for _, tt := range []struct {
		path string
		want any
		ok   bool
	}{
		{path: "backend", want: "podman", ok: true},
		{path: "network.proxy", want: "http://p:1", ok: true},
		{path: "network.allowedDomains", want: []string{"a.com"}, ok: true},
		{path: "egress", want: true, ok: true},
		{path: "env.FOO", want: "bar", ok: true},
		{path: "limits.pids", want: 512, ok: true},
		{path: "limits.memory", ok: false}, // zero value = unset
		{path: "env.MISSING", ok: false},
		{path: "session", ok: false},
		{path: "backend.sub", ok: false}, // descending into a scalar
		{path: "nosuch", ok: false},
	} {
		got, ok := ProfileValue(p, tt.path)
		if ok != tt.ok {
			t.Errorf("ProfileValue(%s) ok = %v, want %v", tt.path, ok, tt.ok)
			continue
		}
		if tt.ok && !reflect.DeepEqual(got, tt.want) {
			t.Errorf("ProfileValue(%s) = %v, want %v", tt.path, got, tt.want)
		}
	}
	if _, ok := ProfileValue(&Profile{}, "egress"); ok {
		t.Error("nil egress should read as unset")
	}
	off := false
	if v, ok := ProfileValue(&Profile{Egress: &off}, "egress"); !ok || v != false {
		t.Errorf("explicit egress: false should read as set: (%v, %v)", v, ok)
	}
}
