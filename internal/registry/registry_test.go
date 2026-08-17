package registry

import (
	"slices"
	"testing"
)

func TestNamesSortedAndComplete(t *testing.T) {
	got := Names()
	want := []string{"claude", "codex", "crush", "dsh", "gemini", "opencode", "pi"}
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
	// pi keeps its config under ~/.pi/agent (PI_CODING_AGENT_DIR's default) and
	// stores OAuth tokens that auto-refresh in auth.json — the same rotating
	// pair claude's .credentials.json is skipped for, so it is skipped too,
	// alongside the per-directory session transcripts.
	pi, _ := Get("pi")
	if len(pi.Seed) != 1 || pi.Seed[0].Path != ".pi/agent" {
		t.Fatalf("pi seed = %+v, want one entry for .pi/agent", pi.Seed)
	}
	for _, want := range []string{"auth.json", "sessions"} {
		if !slices.Contains(pi.Seed[0].Skip, want) {
			t.Errorf(".pi/agent seed must skip %s (got %v)", want, pi.Seed[0].Skip)
		}
	}
	// dsh keeps everything under one root (~/.dsh — DSH_HOME's default), so the
	// seed carries the settings, the home patch layer and the managed
	// .credentials.yaml (a static API key, unlike claude's rotating pair) while
	// leaving the machine-bound parts behind: the transcripts and the web app's
	// storages, plus profiles/node_modules — a symlink farm into the HOST
	// installation's store path, which resolves to nothing in the sandbox.
	dsh, _ := Get("dsh")
	if len(dsh.Seed) != 1 || dsh.Seed[0].Path != ".dsh" {
		t.Fatalf("dsh seed = %+v, want one entry for .dsh", dsh.Seed)
	}
	for _, want := range []string{"sessions", "storages", "profiles/node_modules"} {
		if !slices.Contains(dsh.Seed[0].Skip, want) {
			t.Errorf(".dsh seed must skip %s (got %v)", want, dsh.Seed[0].Skip)
		}
	}
}

// TestResume pins the resume surface the session restore relies on: each
// agent's exact relaunch and picker argvs, ResumeMap carrying every declared
// spec (and only those), and Bins mapping each binary back to its agent.
//
// The argvs are typed into a live shell, so they are pinned per agent against
// the CLI they were read from rather than left to drift: claude
// (--continue/--resume), pi (-c/-r's long forms, pi docs/usage.md), codex
// (`codex resume [--last]`), opencode and crush (--continue; neither ships a
// startup picker, only --session <id>). gemini declares none: its
// checkpointing is a slash command (/chat resume), with no startup flag to
// type. dsh declares none either: its shipped entry modes are the browser UI
// and a one-shot headless job — neither is a conversation a restored pane
// could pick up (its `--resume <id>` example belongs to a terminal app that
// upstream does not ship).
func TestResume(t *testing.T) {
	for _, tc := range []struct {
		agent, bin string
		last, pick []string
	}{
		{agent: "claude", last: []string{"claude", "--continue"}, pick: []string{"claude", "--resume"}},
		{agent: "pi", last: []string{"pi", "--continue"}, pick: []string{"pi", "--resume"}},
		{agent: "codex", last: []string{"codex", "resume", "--last"}, pick: []string{"codex", "resume"}},
		{agent: "opencode", last: []string{"opencode", "--continue"}},
		{agent: "crush", last: []string{"crush", "--continue"}},
		{agent: "gemini"},
		{agent: "dsh"},
	} {
		a, err := Get(tc.agent)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(a.Resume, tc.last) {
			t.Errorf("%s resume = %v, want %v", tc.agent, a.Resume, tc.last)
		}
		if !slices.Equal(a.ResumePick, tc.pick) {
			t.Errorf("%s resumePick = %v, want %v", tc.agent, a.ResumePick, tc.pick)
		}
	}
	// A resume argv is typed into the pane's shell: its first word must be the
	// agent's own binary, or the restore runs something else entirely.
	for _, name := range Names() {
		a, _ := Get(name)
		for _, argv := range [][]string{a.Resume, a.ResumePick} {
			if len(argv) > 0 && argv[0] != a.Bin {
				t.Errorf("%s: resume argv %v does not start with its bin %q", name, argv, a.Bin)
			}
		}
	}
	rm := ResumeMap()
	if !slices.Equal(rm["claude"].Last, []string{"claude", "--continue"}) ||
		!slices.Equal(rm["claude"].Pick, []string{"claude", "--resume"}) {
		t.Errorf("ResumeMap[claude] = %+v", rm["claude"])
	}
	for name, spec := range rm {
		if _, err := Get(name); err != nil {
			t.Fatalf("ResumeMap names unknown agent %q", name)
		}
		if len(spec.Last)+len(spec.Pick) == 0 {
			t.Errorf("%s: empty resume spec in ResumeMap", name)
		}
		for _, w := range append(append([]string{}, spec.Last...), spec.Pick...) {
			if w == "" {
				t.Errorf("%s: resume spec %+v carries an empty word", name, spec)
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
