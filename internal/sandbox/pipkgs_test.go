package sandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// piSettings reads back the sandbox's pi settings for assertions.
func piSettings(t *testing.T, b *Base, slug string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(b.HomeDir(slug), filepath.FromSlash(PiSettingsPath)))
	if err != nil {
		t.Fatalf("read pi settings: %v", err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("pi settings do not parse: %v (%s)", err, data)
	}
	return settings
}

// piPackagesOf returns the settings' package list as strings (the object form
// is reduced to its source), so a test can compare it order-sensitively.
func piPackagesOf(t *testing.T, settings map[string]any) []string {
	t.Helper()
	list, ok := settings["packages"].([]any)
	if !ok {
		t.Fatalf("packages = %#v, want a list", settings["packages"])
	}
	var out []string
	for _, e := range list {
		switch v := e.(type) {
		case string:
			out = append(out, v)
		case map[string]any:
			s, _ := v["source"].(string)
			out = append(out, s)
		default:
			t.Fatalf("package entry %#v has an unexpected shape", e)
		}
	}
	return out
}

// newPiBase builds a sandbox base with a home ready for the pi registration.
func newPiBase(t *testing.T) *Base {
	t.Helper()
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.EnsureHome("s"); err != nil {
		t.Fatal(err)
	}
	return b
}

// TestEnsurePiPackagesWritesFreshSettings: a sandbox that has never run pi has
// no settings file at all — the baked-in packages must still be registered, or
// the image's orchestration package would sit there unused until someone ran
// `pi install` by hand.
func TestEnsurePiPackagesWritesFreshSettings(t *testing.T) {
	b := newPiBase(t)
	var progress strings.Builder
	b.EnsurePiPackages("s", &progress)

	if got := piPackagesOf(t, piSettings(t, b, "s")); len(got) != len(BakedPiPackages) || got[0] != BakedPiPackages[0] {
		t.Errorf("packages = %v, want %v", got, BakedPiPackages)
	}
	if !strings.Contains(progress.String(), "agent-orchestrator") {
		t.Errorf("registration not narrated: %q", progress.String())
	}
	// The home may hold provider configuration — it is not world-readable.
	fi, err := os.Stat(filepath.Join(b.HomeDir("s"), filepath.FromSlash(PiSettingsPath)))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("settings mode = %v (err=%v), want 0600", fi.Mode(), err)
	}
}

// TestEnsurePiPackagesMergesExisting: the settings file is the user's (seeded
// from the host, or written by pi itself) — registering a package must ADD to
// it, never replace it, so a seeded model/theme choice survives.
func TestEnsurePiPackagesMergesExisting(t *testing.T) {
	b := newPiBase(t)
	path := filepath.Join(b.HomeDir("s"), filepath.FromSlash(PiSettingsPath))
	writeFile(t, path, `{"defaultModel":"claude-opus-5","packages":["npm:pi-skills"]}`)

	b.EnsurePiPackages("s", nil)

	settings := piSettings(t, b, "s")
	if settings["defaultModel"] != "claude-opus-5" {
		t.Errorf("defaultModel = %v, want the existing value kept", settings["defaultModel"])
	}
	got := piPackagesOf(t, settings)
	if len(got) != 2 || got[0] != "npm:pi-skills" || got[1] != BakedPiPackages[0] {
		t.Errorf("packages = %v, want the existing entry kept and %q appended", got, BakedPiPackages[0])
	}
}

// TestEnsurePiPackagesNeverDuplicates: the registration runs on every
// create/enter/exec, and pi accepts a package as a bare string OR as the
// object form that filters its resources — a listed package must be
// recognized in both spellings, or every enter would append a copy.
func TestEnsurePiPackagesNeverDuplicates(t *testing.T) {
	for name, existing := range map[string]string{
		"string form": `{"packages":[` + jsonQuote(BakedPiPackages[0]) + `]}`,
		"object form": `{"packages":[{"source":` + jsonQuote(BakedPiPackages[0]) + `,"skills":[]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			b := newPiBase(t)
			path := filepath.Join(b.HomeDir("s"), filepath.FromSlash(PiSettingsPath))
			writeFile(t, path, existing)

			var progress strings.Builder
			b.EnsurePiPackages("s", &progress)
			b.EnsurePiPackages("s", &progress)

			if got := piPackagesOf(t, piSettings(t, b, "s")); len(got) != 1 {
				t.Errorf("packages = %v, want the single existing entry", got)
			}
			// Nothing changed, so nothing is announced either.
			if progress.Len() != 0 {
				t.Errorf("no-op registration narrated: %q", progress.String())
			}
			// An untouched file keeps its bytes — the merge must not rewrite
			// (and reformat) settings it had nothing to add to.
			if data, err := os.ReadFile(path); err != nil || string(data) != existing {
				t.Errorf("settings rewritten: %q (err=%v)", data, err)
			}
		})
	}
}

// TestEnsurePiPackagesLeavesUnparsableSettings: a settings file that does not
// parse is a hand edit (or a half-written file) — it belongs to the user, so
// the registration warns and leaves the bytes alone rather than replacing an
// agent's configuration with our idea of it. Same for a `packages` value of an
// unexpected shape.
func TestEnsurePiPackagesLeavesUnparsableSettings(t *testing.T) {
	for name, existing := range map[string]string{
		"malformed json":  `{"packages": [`,
		"packages string": `{"packages":"npm:pi-skills"}`,
	} {
		t.Run(name, func(t *testing.T) {
			b := newPiBase(t)
			path := filepath.Join(b.HomeDir("s"), filepath.FromSlash(PiSettingsPath))
			writeFile(t, path, existing)

			var progress strings.Builder
			b.EnsurePiPackages("s", &progress)

			if data, err := os.ReadFile(path); err != nil || string(data) != existing {
				t.Errorf("settings = %q (err=%v), want them left untouched", data, err)
			}
			if !strings.Contains(progress.String(), "not registered") {
				t.Errorf("skip not warned about: %q", progress.String())
			}
		})
	}
}

// TestEnsurePiPackagesSurvivesUnwritableHome: an unwritable home must warn and
// move on — pi's package registration is a convenience, never a reason to fail
// the enter that was actually asked for.
func TestEnsurePiPackagesSurvivesUnwritableHome(t *testing.T) {
	b := newPiBase(t)
	home := b.HomeDir("s")
	if err := os.Chmod(home, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(home, 0o700) })

	var progress strings.Builder
	b.EnsurePiPackages("s", &progress)

	if !strings.Contains(progress.String(), "not registered") {
		t.Errorf("failure not warned about: %q", progress.String())
	}
}

// jsonQuote renders a string as a JSON literal for the table above.
func jsonQuote(s string) string {
	q, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(q)
}
