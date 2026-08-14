package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestConfigPath(t *testing.T) {
	// The project config lives at the repo root: sandboxer.nix.
	if got, want := ConfigPath(), ConfigFileName; got != want {
		t.Errorf("ConfigPath() = %q, want %q", got, want)
	}
	if ConfigFileName != "sandboxer.nix" {
		t.Errorf("unexpected config name: %q", ConfigFileName)
	}
}

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"feature-x":    "feature-x",
		"feat/branch":  "feat-branch",
		"  spaces  ":   "spaces",
		"a@@b##c":      "a-b-c",
		"--leading--":  "leading",
		"under_score.": "under_score.",
	}
	for in, want := range cases {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveRuntimePrecedence(t *testing.T) {
	p := &Profile{
		Backend: "podman",
		Egress:  Egress{AllowedDomains: []string{"x.com", "y.com"}, Proxy: "http://p"},
	}
	d := Defaults{Backend: "docker"}

	// Flag override beats profile; profile beats base/defaults.
	rt, err := ResolveRuntime(p, d, "base.com", Overrides{Backend: "flag-engine"})
	if err != nil {
		t.Fatal(err)
	}
	if rt.Backend != "flag-engine" {
		t.Errorf("backend: flag should win over profile, got %q", rt.Backend)
	}
	if !slices.Equal(rt.Domains, []string{"x.com", "y.com"}) {
		t.Errorf("domains: profile should win, got %v", rt.Domains)
	}
	if rt.Proxy != "http://p" {
		t.Errorf("proxy not carried: %q", rt.Proxy)
	}
	if !rt.Egress {
		t.Error("egress should default true")
	}

	// Nil profile, no overrides → defaults + base domains.
	rt2, err := ResolveRuntime(nil, d, "base.com,two.com", Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if rt2.Backend != "docker" {
		t.Errorf("backend default = %q", rt2.Backend)
	}
	if !slices.Equal(rt2.Domains, []string{"base.com", "two.com"}) {
		t.Errorf("base domains = %v", rt2.Domains)
	}
}

// TestResolveRuntimeLimits pins the resource-cap resolution: a profile's
// limits: overrides the SANDBOXER_MEM/SANDBOXER_CPU env defaults, and
// memory/cpus fall back to those defaults when the profile is silent.
func TestResolveRuntimeLimits(t *testing.T) {
	// Profile limits win over the env defaults.
	p := &Profile{Limits: Limits{Memory: "4G", CPUs: "2"}}
	rt, err := ResolveRuntime(p, Defaults{Mem: "1G", CPU: "1"}, "base.com", Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if rt.Mem != "4G" || rt.CPU != "2" {
		t.Errorf("profile limits should win: mem=%q cpu=%q", rt.Mem, rt.CPU)
	}

	// No profile limits → the env defaults apply.
	rt2, err := ResolveRuntime(&Profile{}, Defaults{Mem: "1G", CPU: "1"}, "base.com", Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if rt2.Mem != "1G" || rt2.CPU != "1" {
		t.Errorf("env limits should apply: mem=%q cpu=%q", rt2.Mem, rt2.CPU)
	}
}

// TestAnnotateRemovedKeys: a config that still uses a retired key trips the
// strict decoder, and annotateRemovedKeys upgrades the terse "field X not found"
// into a migration hint carrying the key and its guidance — on both the flat
// (Load) and document (LoadDocument) decode paths. Table-driven over the whole
// removedKeys table so a newly-retired key is covered automatically.
func TestAnnotateRemovedKeys(t *testing.T) {
	if len(removedKeys) == 0 {
		t.Skip("no removed keys yet")
	}
	for key, hint := range removedKeys {
		t.Run(key, func(t *testing.T) {
			dir := t.TempDir()
			flat := writeFile(t, dir, "flat.nix", "{ "+key+" = \"whatever\"; }\n")
			_, err := LoadDocument(flat)
			if err == nil {
				t.Fatalf("a removed key %q must still be rejected", key)
			}
			for _, want := range []string{key, hint} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("flat error %q missing %q", err, want)
				}
			}
			doc := writeFile(t, dir, "doc.nix", "{ profiles.web."+key+" = \"whatever\"; }\n")
			if _, err := LoadDocument(doc); err == nil || !strings.Contains(err.Error(), hint) {
				t.Errorf("LoadDocument for removed key %q = %v, want the migration hint", key, err)
			}
		})
	}
}

// TestEgressBoolMigrationHint: `egress` used to be a top-level bool and is now
// the egress attrset, so an old `egress = false` trips a type error (bool into
// struct) rather than an unknown-field one. annotateRemovedKeys must still turn
// it into the actionable egress.enabled migration hint.
func TestEgressBoolMigrationHint(t *testing.T) {
	dir := t.TempDir()
	cfg := writeFile(t, dir, "old.nix", "{ name = \"x\"; egress = false; }\n")
	_, err := LoadDocument(cfg)
	if err == nil {
		t.Fatal("a bool `egress = false` must be rejected now that egress is an attrset")
	}
	if !strings.Contains(err.Error(), "egress.enabled = false") {
		t.Errorf("error %q missing the egress.enabled migration hint", err)
	}
}

func TestValidateProxy(t *testing.T) {
	cases := []struct {
		name string
		url  string
		ok   bool
	}{
		{"empty", "", true},
		{"valid http", "http://proxy.corp:3128", true},
		{"valid https", "https://p:3128", true},
		{"scheme-less rejected", "p:3128", false},
		{"hostless rejected", "http://", false},
		{"unparseable rejected", "http://%zz", false},
		{"socks rejected", "socks5://p:1080", false},
	}
	for _, c := range cases {
		err := ValidateProxy(c.url)
		if (err == nil) != c.ok {
			t.Errorf("%s: ValidateProxy err=%v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestResolveRuntimeProxy(t *testing.T) {
	// A single proxy URL is carried into Runtime and keeps egress on (chained).
	p := &Profile{Egress: Egress{Proxy: "http://proxy.corp:3128"}}
	rt, err := ResolveRuntime(p, Defaults{}, "base.com", Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if rt.Proxy != "http://proxy.corp:3128" {
		t.Errorf("proxy not carried into Runtime: %q", rt.Proxy)
	}
	if !rt.Egress {
		t.Error("a proxy must not silently disable egress (the resolved flag still reports on)")
	}

	// SANDBOXER_PROXY (Defaults.Proxy) is the lowest-precedence fallback.
	rt2, err := ResolveRuntime(&Profile{}, Defaults{Proxy: "http://env:9999"}, "base.com", Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if rt2.Proxy != "http://env:9999" {
		t.Errorf("env proxy default not applied: %q", rt2.Proxy)
	}
	// A profile proxy beats the env default.
	rt3, err := ResolveRuntime(&Profile{Egress: Egress{Proxy: "http://prof:1"}}, Defaults{Proxy: "http://env:9999"}, "base.com", Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if rt3.Proxy != "http://prof:1" {
		t.Errorf("profile proxy should beat env default: %q", rt3.Proxy)
	}

	// An https proxy is fine in every egress state — the guest talks to the
	// proxy directly, there is no chaining sidecar.
	hp := &Profile{Egress: Egress{Proxy: "https://p:3128"}}
	if _, err := ResolveRuntime(hp, Defaults{}, "base.com", Overrides{}); err != nil {
		t.Errorf("ResolveRuntime must accept an https proxy: %v", err)
	}
}

func TestEgressDisabled(t *testing.T) {
	off, on := false, true
	if (&Profile{Egress: Egress{Enabled: &off}}).EgressEnabled() {
		t.Error("egress.enabled = false should disable the allowlist")
	}
	if !(&Profile{}).EgressEnabled() {
		t.Error("default (enabled unset) should enable the allowlist")
	}
	if !(&Profile{Egress: Egress{Enabled: &on}}).EgressEnabled() {
		t.Error("egress.enabled = true should enable the allowlist")
	}
}

func TestEgressDisabledResolution(t *testing.T) {
	off := false
	// egress.enabled = false: an https proxy is legal and the resolved Egress
	// flag reports off.
	p := &Profile{Egress: Egress{
		Enabled:        &off,
		Proxy:          "https://corp:8080",
		AllowedDomains: []string{"github.com"},
	}}
	rt, err := ResolveRuntime(p, Defaults{}, "base.com", Overrides{})
	if err != nil {
		t.Fatalf("direct mode should accept an https proxy: %v", err)
	}
	if rt.Egress {
		t.Error("egress.enabled = false must resolve to Egress off")
	}
}

func TestLoadAndJSONRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ConfigFileName)
	nix := `{
  name = "feature-x";
  backend = "podman";
  egress.allowedDomains = [ "api.anthropic.com" "registry.npmjs.org" ];
  srcs = [
    { src = "."; include = [ "/src/lib/" ]; }
    { src = "../shared-lib"; branch = "feat/x"; }
  ];
}
`
	if err := os.WriteFile(path, []byte(nix), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := LoadDocument(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := d.Select("")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "feature-x" || p.Backend != "podman" {
		t.Errorf("scalars wrong: %+v", p)
	}
	if len(p.Egress.AllowedDomains) != 2 {
		t.Errorf("domains: %v", p.Egress.AllowedDomains)
	}
	if len(p.Srcs) != 2 || p.Srcs[1].Branch != "feat/x" || p.Srcs[0].Include[0] != "/src/lib/" {
		t.Errorf("srcs: %+v", p.Srcs)
	}
	// Relative src paths stay relative in the snapshot: they resolve against
	// the PROJECT ROOT at sandbox-sync time, not against the profile file.
	if p.Srcs[0].Src != "." || p.Srcs[1].Src != "../shared-lib" {
		t.Errorf("srcs should stay relative: %+v", p.Srcs)
	}
	// JSON serialization uses camelCase keys the container and sandbox read.
	b, err := p.JSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"srcs"`, `"allowedDomains"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("JSON missing %s:\n%s", want, b)
		}
	}
}

func TestExampleProfilesParse(t *testing.T) {
	// The shipped examples must stay valid under the strict schema. No Skip:
	// a renamed example must fail here, not silently vanish from coverage.
	for _, name := range []string{"config.nix", "with-srcs.nix", "custom-image.nix", "multi-profile.nix"} {
		path := filepath.Join("..", "..", "examples", name)
		if _, err := LoadDocument(path); err != nil {
			t.Errorf("example %s failed to load: %v", name, err)
		}
	}
}

func TestLoadUnknownFieldRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.nix")
	if err := os.WriteFile(path, []byte("{ name = \"x\"; bogusField = 1; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDocument(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Errorf("unknown attr = %v, want a strict unknown-field error", err)
	}
}

// TestValidateInclude pins the shape an include pattern must have for a bind
// mount to honor it: an anchored, slash-terminated, glob-free directory. The
// rejected shapes are the ones a mount CANNOT express, so each is refused with
// a message that names the directory form rather than failing later and
// vaguely.
func TestValidateInclude(t *testing.T) {
	for _, tc := range []struct {
		name    string
		include []string
		wantErr string // "" = accepted; else a substring the message must carry
	}{
		{name: "empty is the whole repo", include: nil},
		{name: "double-star is the whole repo", include: []string{"**"}},
		{name: "one directory", include: []string{"/src/proto/"}},
		{name: "several directories", include: []string{"/src/proto/", "/shared/lib/"}},
		{name: "deep directory", include: []string{"/a/b/c/d/"}},
		{name: "dash and dot in a name", include: []string{"/my-svc/v1.2/"}},
		// A trailing slash is optional — "/api" and "/api/" mean the same
		// directory. Requiring it was pure friction: whether the path is a file
		// or a directory is decided by stat'ing it, not by the syntax.
		{name: "no trailing slash", include: []string{"/api"}},
		{name: "no trailing slash, deep", include: []string{"/src/proto"}},
		{name: "mixed trailing slashes", include: []string{"/api", "/web/"}},
		// A bare path that turns out to be a FILE is accepted by the syntax and
		// rejected later by sandbox.checkViewDirs (which stats it) — see
		// TestCheckViewDirsRejectsAFile.
		{name: "bare path (file-ness checked at materialize)", include: []string{"/go.mod"}},
		// A directory and a child of it is redundant, not invalid: overlap is not
		// checked here (each pattern is validated alone), and the parent already
		// exposes the child. sandbox.Mounts turns both into nested bind mounts.
		{name: "parent and child (redundant, allowed)", include: []string{"/src/", "/src/proto/"}},

		// Ant-style directory patterns: *, ? and [...] within a segment
		// (path.Match against directory names) and a whole "**" segment for any
		// depth. They can only ever select DIRECTORIES — whether anything
		// matches is decided against the worktree on disk, not here.
		{name: "star segment", include: []string{"/services/*/"}},
		{name: "question mark segment", include: []string{"/src?/"}},
		{name: "bracket segment", include: []string{"/src[ab]/"}},
		{name: "any-depth directory", include: []string{"/**/proto/"}},
		{name: "unanchored double-star sugar", include: []string{"**/proto/"}},
		{name: "glob leaf (matches dirs only, never files)", include: []string{"**/*.md"}},
		{name: "trailing double-star (same as the parent alone)", include: []string{"/a/**"}},
		{name: "redundant double-double-star", include: []string{"/**/**/x/"}},
		{name: "star inside a segment is not recursive", include: []string{"/a**/"}},
		{name: "pattern among literals", include: []string{"/ok/", "**/*.md"}},

		{name: "unclosed bracket", include: []string{"/src[ab/"}, wantErr: "bad pattern segment"},
		{name: "double-star alone among entries", include: []string{"/ok/", "/**/"}, wantErr: "the whole repo"},
		{name: "anchored bare double-star", include: []string{"/ok/", "/**"}, wantErr: "the whole repo"},
		{name: "negation", include: []string{"!/vendor/"}, wantErr: "negation is not supported"},
		{name: "unanchored", include: []string{"src/proto/"}, wantErr: "must be anchored"},
		{name: "unanchored no slash", include: []string{"api"}, wantErr: "must be anchored"},
		{name: "unanchored glob", include: []string{"*.md"}, wantErr: "must be anchored"},
		{name: "root", include: []string{"/"}, wantErr: "the whole repo"},
		{name: "empty pattern", include: []string{""}, wantErr: "empty pattern"},
		{name: "parent escape", include: []string{"/../etc/"}, wantErr: "plain repo-relative"},
		{name: "dot segment", include: []string{"/a/./b/"}, wantErr: "plain repo-relative"},
		{name: "double slash", include: []string{"/a//b/"}, wantErr: "plain repo-relative"},
		{name: "one bad among good", include: []string{"/ok/", "src/x/"}, wantErr: "must be anchored"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateInclude(tc.include)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateInclude(%v) = %v, want accepted", tc.include, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateInclude(%v) accepted, want an error mentioning %q", tc.include, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("ValidateInclude(%v) = %q, want it to mention %q", tc.include, err, tc.wantErr)
			}
		})
	}
}

// TestValidateGit covers the git-share modes and the one combination that is
// refused: a shared git dir alongside a narrowing include, where the two keys
// would claim opposite things about the same source.
func TestValidateGit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    string
		include []string
		wantErr string // "" = accepted; else a substring the message must carry
	}{
		{name: "absent is off", mode: ""},
		{name: "explicit off", mode: GitOff},
		{name: "read-only", mode: GitRO},
		{name: "read-write", mode: GitRW},
		{name: "off alongside include", mode: GitOff, include: []string{"/src/"}},
		{name: "shared with an explicit whole-repo include", mode: GitRO, include: []string{"**"}},

		{name: "unknown mode", mode: "yes", wantErr: "is not a mode"},
		{name: "mode is not a bool", mode: "true", wantErr: "is not a mode"},
		{name: "case matters", mode: "RO", wantErr: "is not a mode"},
		{name: "ro plus include", mode: GitRO, include: []string{"/src/"}, wantErr: "cannot be combined with include"},
		{name: "rw plus include", mode: GitRW, include: []string{"/src/"}, wantErr: "cannot be combined with include"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateGit(tc.mode, tc.include)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateGit(%q, %v) = %v, want accepted", tc.mode, tc.include, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateGit(%q, %v) accepted, want an error mentioning %q", tc.mode, tc.include, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("ValidateGit(%q, %v) = %q, want it to mention %q", tc.mode, tc.include, err, tc.wantErr)
			}
		})
	}
}

// TestValidateSrcsChecksGit pins that the per-source git mode is reached by the
// profile-wide validation — `config validate` must catch it, not just enter.
func TestValidateSrcsChecksGit(t *testing.T) {
	err := ValidateSrcs([]Src{
		{Src: ".", Branch: "main"},
		{Src: "../lib", Branch: "main", Git: GitRW, Include: []string{"/src/"}},
	})
	if err == nil || !strings.Contains(err.Error(), "cannot be combined with include") {
		t.Fatalf("ValidateSrcs = %v, want the git/include conflict reported", err)
	}
}

// TestGitShared: only the two sharing modes share anything.
func TestGitShared(t *testing.T) {
	for mode, want := range map[string]bool{"": false, GitOff: false, GitRO: true, GitRW: true, "nonsense": false} {
		if got := GitShared(mode); got != want {
			t.Errorf("GitShared(%q) = %v, want %v", mode, got, want)
		}
	}
}

// TestWholeRepo: only an absent or explicitly catch-all include means "no
// narrowing" — anything else puts the sandbox on view mounts.
func TestWholeRepo(t *testing.T) {
	for _, tc := range []struct {
		include []string
		want    bool
	}{
		{nil, true},
		{[]string{}, true},
		{[]string{"**"}, true},
		{[]string{"/src/"}, false},
		{[]string{"**", "/src/"}, false}, // not the single catch-all
	} {
		if got := WholeRepo(tc.include); got != tc.want {
			t.Errorf("WholeRepo(%v) = %v, want %v", tc.include, got, tc.want)
		}
	}
}

// TestResolveRuntimeValidatesSrcs: a bad include is refused wherever a profile
// is resolved, so `config validate` catches it without touching git.
func TestResolveRuntimeValidatesSrcs(t *testing.T) {
	p := &Profile{Srcs: []Src{{Src: ".", Branch: "feat/x", Include: []string{"*.md"}}}}
	if _, err := ResolveRuntime(p, Defaults{}, "", Overrides{}); err == nil ||
		!strings.Contains(err.Error(), "must be anchored") {
		t.Errorf("ResolveRuntime = %v, want an unanchored-include refusal", err)
	}
}
