package config

import (
	"testing"
)

func TestProfileName(t *testing.T) {
	if got := ProfileName("/x/web.yaml", nil); got != "web" {
		t.Errorf("base-name fallback = %q, want web", got)
	}
	if got := ProfileName("/x/web.yaml", &Profile{Name: "prod"}); got != "prod" {
		t.Errorf("explicit name: should override = %q, want prod", got)
	}
	if got := ProfileName("/x/web.yml", &Profile{}); got != "web" {
		t.Errorf(".yml stem = %q, want web", got)
	}
}

func TestListProfiles(t *testing.T) {
	dir := t.TempDir()
	// Multi-profile doc: every section listed, default: marked, sorted by name.
	cfg := writeFile(t, dir, "project.nix",
		"{ default = \"web\"; profiles = { web.backend = \"docker\"; db.backend = \"podman\"; }; }\n")
	got := ListProfiles(cfg)
	if len(got) != 2 || got[0].Name != "db" || got[1].Name != "web" {
		t.Fatalf("ListProfiles = %+v, want [db web]", got)
	}
	if !got[1].IsDefault || got[0].IsDefault {
		t.Errorf("default marking wrong: %+v", got)
	}
	if got[0].Backend != "podman" || got[1].Path != cfg {
		t.Errorf("entry fields wrong: %+v", got)
	}

	// Flat file: its single profile, flagged as the (sole) default.
	flat := writeFile(t, dir, "feat.nix", "{ backend = \"docker\"; }\n")
	if got := ListProfiles(flat); len(got) != 1 || got[0].Name != "feat" || !got[0].IsDefault {
		t.Errorf("flat ListProfiles = %+v", got)
	}

	// Empty path, missing and unparseable files contribute nothing.
	if got := ListProfiles(""); got != nil {
		t.Errorf("empty path = %+v, want nil", got)
	}
	if got := ListProfiles(dir + "/nope.nix"); got != nil {
		t.Errorf("missing file = %+v, want nil", got)
	}
	bad := writeFile(t, dir, "bad.nix", "{ bogusField = 1; }\n")
	if got := ListProfiles(bad); got != nil {
		t.Errorf("unparseable file = %+v, want nil", got)
	}
}
