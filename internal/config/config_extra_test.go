package config

import (
	"slices"
	"testing"
)

func TestLoadDefaultsFromEnv(t *testing.T) {
	t.Setenv("SANDBOXER_BACKEND", "docker")
	t.Setenv("SANDBOXER_DOMAINS", "a.com")
	t.Setenv("SANDBOXER_IMAGE", "img:1")
	t.Setenv("SANDBOXER_ENGINE", "docker")
	t.Setenv("SANDBOXER_MEM", "2G")
	t.Setenv("SANDBOXER_CPU", "50%")

	d := LoadDefaults()
	if d.Backend != "docker" || d.Domains != "a.com" ||
		d.Image != "img:1" || d.Engine != "docker" ||
		d.Mem != "2G" || d.CPU != "50%" {
		t.Errorf("LoadDefaults from env = %+v", d)
	}
}

func TestLoadDefaultsBare(t *testing.T) {
	for _, k := range []string{
		"SANDBOXER_BACKEND", "SANDBOXER_DOMAINS",
		"SANDBOXER_IMAGE", "SANDBOXER_ENGINE", "SANDBOXER_MEM", "SANDBOXER_CPU",
	} {
		t.Setenv(k, "")
	}

	d := LoadDefaults()
	if d.Backend != "docker" || d.Domains != DefaultDomains || d.Image != DefaultImage {
		t.Errorf("bare defaults = %+v", d)
	}
}

func TestDomainsCSV(t *testing.T) {
	if got := (Runtime{Domains: []string{"a", "b"}}).DomainsCSV(); got != "a,b" {
		t.Errorf("DomainsCSV = %q", got)
	}
	if got := (Runtime{}).DomainsCSV(); got != "" {
		t.Errorf("empty DomainsCSV = %q", got)
	}
}

func TestResolveRuntimeDomainsPrecedence(t *testing.T) {
	// Flag CSV wins and is trimmed/split.
	rt, err := ResolveRuntime(&Profile{Egress: Egress{AllowedDomains: []string{"p.com"}}},
		Defaults{}, "base.com", Overrides{Domains: "a.com, , b.com"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rt.Domains, []string{"a.com", "b.com"}) {
		t.Errorf("flag domains = %v", rt.Domains)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := LoadDocument("/no/such/profile.nix"); err == nil {
		t.Error("LoadDocument of a missing file should error")
	}
}

func TestValidateBackend(t *testing.T) {
	// The container backends and an empty (auto-detect) value are accepted.
	for _, be := range []string{"", "auto", "podman", "docker"} {
		if err := ValidateBackend(Runtime{Backend: be}); err != nil {
			t.Errorf("backend %q should be allowed: %v", be, err)
		}
	}
	// The removed native backend gets a clear migration error.
	if err := ValidateBackend(Runtime{Backend: "native"}); err == nil {
		t.Error("native backend should be rejected (removed)")
	}
	// Any other value is rejected too.
	if err := ValidateBackend(Runtime{Backend: "qemu"}); err == nil {
		t.Error("unknown backend should be rejected")
	}
}

func TestValidateBackendMicrovm(t *testing.T) {
	// A plain microvm backend (optionally with an allowlist) is accepted.
	if err := ValidateBackend(Runtime{Backend: "microvm", Egress: true, Domains: []string{"a.com"}}); err != nil {
		t.Errorf("plain microvm should be allowed: %v", err)
	}
	// The squid-only egress features have no smolvm analogue → hard errors.
	for _, rt := range []Runtime{
		{Backend: "microvm", Proxy: "http://p:3128"},
		{Backend: "microvm", NoProxy: "localhost"},
		{Backend: "microvm", Routes: []Route{{}}},
	} {
		if err := ValidateBackend(rt); err == nil {
			t.Errorf("microvm should reject %+v", rt)
		}
	}
}

func TestValidateDomains(t *testing.T) {
	// Valid domains pass.
	if err := ValidateDomains([]string{"api.anthropic.com", "github.com"}); err != nil {
		t.Errorf("valid domains should pass: %v", err)
	}
	// Empty list is fine.
	if err := ValidateDomains(nil); err != nil {
		t.Error("nil domains should pass")
	}
	// Missing dot.
	if err := ValidateDomains([]string{"localhost"}); err == nil {
		t.Error("localhost should fail (missing dot)")
	}
	// Whitespace.
	if err := ValidateDomains([]string{"api .com"}); err == nil {
		t.Error("spaces should fail")
	}
	// URL prefix.
	if err := ValidateDomains([]string{"https://api.example.com"}); err == nil {
		t.Error("URL prefix should fail")
	}
	// Path.
	if err := ValidateDomains([]string{"api.example.com/v1"}); err == nil {
		t.Error("path should fail")
	}
	// The leading-dot subdomain form is accepted.
	if err := ValidateDomains([]string{".anthropic.com"}); err != nil {
		t.Errorf("leading-dot domain should pass: %v", err)
	}
	// Security: a value carrying a control char (newline/tab/CR) would inject a
	// squid.conf directive; a bare/only-dot value would match every host. All
	// must be rejected before they can reach egress.squidConf.
	for _, bad := range []string{
		"a.com\nhttp_access allow all",
		"a.com\thttp_access\tallow\tall",
		"a.com\r\nhttp_access allow all",
		".",
		"..",
		"under_score.com",
	} {
		if err := ValidateDomains([]string{bad}); err == nil {
			t.Errorf("ValidateDomains(%q) = nil, want rejection (injection / degenerate domain)", bad)
		}
	}
}
