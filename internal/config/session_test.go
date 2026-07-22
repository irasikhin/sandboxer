package config

import (
	"strings"
	"testing"
)

// TestResolveRuntimeSessionPrecedence pins the session resolution chain:
// flag → env (SANDBOXER_SESSION via Defaults) → profile → "persistent". The
// env deliberately sits above the profile (operator kill-switch).
func TestResolveRuntimeSessionPrecedence(t *testing.T) {
	cases := []struct {
		name    string
		flag    string
		env     string
		profile string
		want    string
	}{
		{"default persistent", "", "", "", SessionPersistent},
		{"profile wins over default", "", "", SessionEphemeral, SessionEphemeral},
		{"env wins over profile", "", SessionEphemeral, SessionPersistent, SessionEphemeral},
		{"flag wins over env", SessionEphemeral, SessionPersistent, "", SessionEphemeral},
		{"flag wins over all", SessionEphemeral, SessionPersistent, SessionPersistent, SessionEphemeral},
	}
	for _, c := range cases {
		rt, err := ResolveRuntime(&Profile{Session: c.profile}, Defaults{Session: c.env}, "",
			Overrides{Session: c.flag})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if rt.Session != c.want {
			t.Errorf("%s: session = %q, want %q", c.name, rt.Session, c.want)
		}
	}
}

// TestValidateSession accepts only the two known modes; the resolved value is
// never empty, so "" is a misuse and rejected like any typo.
func TestValidateSession(t *testing.T) {
	for _, ok := range []string{SessionPersistent, SessionEphemeral} {
		if err := ValidateSession(Runtime{Session: ok}); err != nil {
			t.Errorf("ValidateSession(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "bogus", "Persistent", "one-shot"} {
		err := ValidateSession(Runtime{Session: bad})
		if err == nil {
			t.Errorf("ValidateSession(%q) = nil, want error", bad)
			continue
		}
		if !strings.Contains(err.Error(), "unknown session mode") {
			t.Errorf("ValidateSession(%q) error = %v", bad, err)
		}
	}
}

// TestLoadDefaultsSession: the raw env value lands in Defaults (no default
// here — the "persistent" fallback is applied at resolve time, below the
// profile).
func TestLoadDefaultsSession(t *testing.T) {
	t.Setenv("SANDBOXER_SESSION", SessionEphemeral)
	if d := LoadDefaults(); d.Session != SessionEphemeral {
		t.Errorf("Session from env = %q", d.Session)
	}
	t.Setenv("SANDBOXER_SESSION", "")
	if d := LoadDefaults(); d.Session != "" {
		t.Errorf("unset SANDBOXER_SESSION must stay empty, got %q", d.Session)
	}
}

// TestResolveRuntimeAutoResume pins the restore auto-resume gate: default on,
// profile `autoResume = false` turns it off, and the SANDBOXER_NO_RESUME=1
// kill-switch (Defaults.NoResume) wins over a profile that turned it on —
// the same env-above-profile shape as the session mode.
func TestResolveRuntimeAutoResume(t *testing.T) {
	on, off := true, false
	cases := []struct {
		name     string
		profile  *bool
		noResume bool
		want     bool
	}{
		{"default on", nil, false, true},
		{"profile off", &off, false, false},
		{"profile on", &on, false, true},
		{"env kills default", nil, true, false},
		{"env kills profile on", &on, true, false},
	}
	for _, c := range cases {
		rt, err := ResolveRuntime(&Profile{AutoResume: c.profile}, Defaults{NoResume: c.noResume}, "", Overrides{})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if rt.AutoResume != c.want {
			t.Errorf("%s: AutoResume = %v, want %v", c.name, rt.AutoResume, c.want)
		}
	}
}

// TestAutoResumeEnabled mirrors EgressEnabled: nil means on.
func TestAutoResumeEnabled(t *testing.T) {
	on, off := true, false
	if (&Profile{AutoResume: &off}).AutoResumeEnabled() {
		t.Error("autoResume=false must disable")
	}
	if !(&Profile{}).AutoResumeEnabled() {
		t.Error("nil autoResume must default on")
	}
	if !(&Profile{AutoResume: &on}).AutoResumeEnabled() {
		t.Error("autoResume=true must enable")
	}
}

// TestLoadDefaultsNoResume: the env kill-switch is read strictly as "1".
func TestLoadDefaultsNoResume(t *testing.T) {
	t.Setenv("SANDBOXER_NO_RESUME", "1")
	if d := LoadDefaults(); !d.NoResume {
		t.Error("SANDBOXER_NO_RESUME=1 must set NoResume")
	}
	t.Setenv("SANDBOXER_NO_RESUME", "")
	if d := LoadDefaults(); d.NoResume {
		t.Error("unset SANDBOXER_NO_RESUME must stay off")
	}
}

// TestProfileAutoResumeDecode: the `autoResume` field survives the strict
// decode and the JSON snapshot.
func TestProfileAutoResumeDecode(t *testing.T) {
	p, err := decodeProfileJSON([]byte(`{"name":"x","autoResume":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.AutoResumeEnabled() {
		t.Error("decoded autoResume=false must disable")
	}
	data, err := p.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"autoResume": false`) {
		t.Errorf("JSON snapshot missing autoResume field:\n%s", data)
	}
}

// TestLoadDocumentFlatSession pins the full file path: a flat profile with
// `session: ephemeral` keeps the field through LoadDocument + Select (the
// route every file-based profile takes).
func TestLoadDocumentFlatSession(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "feat.nix", "{ session = \"ephemeral\"; }\n")
	d, err := LoadDocument(p)
	if err != nil {
		t.Fatal(err)
	}
	prof, err := d.Select("")
	if err != nil {
		t.Fatal(err)
	}
	if prof.Session != SessionEphemeral {
		t.Errorf("flat session = %q, want %q", prof.Session, SessionEphemeral)
	}
}

// TestProfileSessionDecode: the `session` field round-trips through the
// strict decode and the JSON snapshot.
func TestProfileSessionDecode(t *testing.T) {
	p, err := decodeProfileJSON([]byte(`{"name":"x","session":"ephemeral"}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.Session != SessionEphemeral {
		t.Errorf("decoded session = %q", p.Session)
	}
	data, err := p.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"session": "ephemeral"`) {
		t.Errorf("JSON snapshot missing session field:\n%s", data)
	}
}
