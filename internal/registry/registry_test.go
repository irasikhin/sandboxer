package registry

import (
	"slices"
	"testing"
)

func TestNamesSortedAndComplete(t *testing.T) {
	got := Names()
	want := []string{"aider", "claude", "codex", "crush", "gemini", "opencode", "pi"}
	if !slices.Equal(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
}

func TestGetUnknown(t *testing.T) {
	if _, err := Get("nope"); err == nil {
		t.Fatal("expected error for unknown agent")
	}
}

// TestAgentFields pins the surviving catalog surface: every agent has a binary,
// its auth env vars, and a nix package (the fields the Nix flake and auth
// passthrough actually read).
func TestAgentFields(t *testing.T) {
	for _, name := range Names() {
		a, err := Get(name)
		if err != nil {
			t.Fatal(err)
		}
		if a.Bin == "" {
			t.Errorf("%s: empty bin", name)
		}
		if a.NixPackage == "" {
			t.Errorf("%s: empty nixPackage", name)
		}
		if len(a.AuthEnv) == 0 {
			t.Errorf("%s: no authEnv", name)
		}
	}
}

// TestSeedPaths pins the seed surface hostConfigs relies on: every declared
// seed path is home-relative (no leading '/' or '..'), and claude — the
// primary agent — seeds both its config dir and the top-level state file.
func TestSeedPaths(t *testing.T) {
	for _, name := range Names() {
		a, err := Get(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, sp := range a.Seed {
			if sp.Path == "" || sp.Path[0] == '/' || sp.Path == ".." {
				t.Errorf("%s: seed path %q must be home-relative", name, sp.Path)
			}
		}
	}
	claude, _ := Get("claude")
	var paths []string
	for _, sp := range claude.Seed {
		paths = append(paths, sp.Path)
	}
	for _, want := range []string{".claude", ".claude.json"} {
		if !slices.Contains(paths, want) {
			t.Errorf("claude seed misses %q (got %v)", want, paths)
		}
	}
	// the bulky, private transcripts dir stays behind — and so does the
	// rotating OAuth pair, which cannot survive as a copy (refresh-token
	// rotation kills one side; long-lived tokens travel via authEnv instead)
	for _, sp := range claude.Seed {
		if sp.Path != ".claude" {
			continue
		}
		for _, want := range []string{"projects", ".credentials.json"} {
			if !slices.Contains(sp.Skip, want) {
				t.Errorf(".claude seed must skip %s (got %v)", want, sp.Skip)
			}
		}
	}
}

// TestResume pins the resume surface the session restore relies on: claude's
// exact relaunch argv, ResumeMap carrying every declared argv (and only
// those), and Bins mapping each binary back to its agent.
func TestResume(t *testing.T) {
	claude, _ := Get("claude")
	if !slices.Equal(claude.Resume, []string{"claude", "--continue"}) {
		t.Errorf("claude resume = %v, want [claude --continue]", claude.Resume)
	}
	rm := ResumeMap()
	if !slices.Equal(rm["claude"], []string{"claude", "--continue"}) {
		t.Errorf("ResumeMap[claude] = %v", rm["claude"])
	}
	for name, argv := range rm {
		a, err := Get(name)
		if err != nil {
			t.Fatalf("ResumeMap names unknown agent %q", name)
		}
		if len(argv) == 0 {
			t.Errorf("%s: empty resume argv in ResumeMap", name)
		}
		for _, w := range argv {
			if w == "" {
				t.Errorf("%s: resume argv %v carries an empty word", name, a.Resume)
			}
		}
	}
	bins := Bins()
	if len(bins) != len(Names()) {
		t.Fatalf("Bins has %d entries, want one per agent (%d) — a duplicated bin would shadow an agent", len(bins), len(Names()))
	}
	for _, name := range Names() {
		a, _ := Get(name)
		if bins[a.Bin] != name {
			t.Errorf("Bins[%s] = %q, want %q", a.Bin, bins[a.Bin], name)
		}
	}
}

func TestCodexExcludedFromImage(t *testing.T) {
	a, err := Get("codex")
	if err != nil {
		t.Fatal(err)
	}
	if a.Image == nil || *a.Image {
		t.Error("codex should declare image:false")
	}
	claude, _ := Get("claude")
	if claude.Image != nil {
		t.Error("claude should default to image (nil pointer)")
	}
}
