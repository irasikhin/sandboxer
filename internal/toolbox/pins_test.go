package toolbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pinsCacheDir isolates the pins file under a temp XDG_CACHE_HOME and returns
// the directory, so no test touches the user's real cache.
func pinsCacheDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	return dir
}

// TestPinsRoundtrip: a cold cache loads as empty pins (no error), SavePins
// stamps atomically under <cache>/sandboxer/image-pins.json, and a malformed
// file is a fail-closed error naming the path.
func TestPinsRoundtrip(t *testing.T) {
	cache := pinsCacheDir(t)

	cold, err := LoadPins()
	if err != nil || len(cold) != 0 {
		t.Fatalf("cold LoadPins = %v, %v; want empty pins, nil error", cold, err)
	}

	want := Pins{
		"nixpkgs":    {Ref: "refs/heads/nixos-unstable", Rev: strings.Repeat("a", 40), ResolvedAt: "2026-06-10T00:00:00Z"},
		"llm-agents": {Ref: "HEAD", Rev: strings.Repeat("b", 40)},
	}
	if err := SavePins(want); err != nil {
		t.Fatalf("SavePins: %v", err)
	}
	path, err := PinsPath()
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(cache, "sandboxer", pinsFileName) {
		t.Errorf("PinsPath = %q, want it under XDG_CACHE_HOME/sandboxer", path)
	}
	got, err := LoadPins()
	if err != nil {
		t.Fatalf("LoadPins: %v", err)
	}
	if got["nixpkgs"] != want["nixpkgs"] || got["llm-agents"] != want["llm-agents"] {
		t.Errorf("roundtrip = %+v, want %+v", got, want)
	}

	// Malformed pins are an error naming the path, never silently dropped.
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPins(); err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("malformed pins = %v, want an error naming %s", err, path)
	}
}

// TestPinsPathError: with no HOME and no XDG_CACHE_HOME there is no cache dir
// to stamp into — every pins entry point fails loudly.
func TestPinsPathError(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "")
	if _, err := PinsPath(); err == nil {
		t.Error("PinsPath without a cache dir must error")
	}
	if _, err := LoadPins(); err == nil {
		t.Error("LoadPins without a cache dir must error")
	}
	if err := SavePins(Pins{}); err == nil {
		t.Error("SavePins without a cache dir must error")
	}
}

// TestSavePinsBlockedDir: a file squatting where the sandboxer cache dir must
// go fails the stamp instead of silently dropping it.
func TestSavePinsBlockedDir(t *testing.T) {
	cache := pinsCacheDir(t)
	if err := os.WriteFile(filepath.Join(cache, "sandboxer"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SavePins(Pins{"nixpkgs": {Rev: strings.Repeat("a", 40)}}); err == nil {
		t.Error("blocked cache dir must error")
	}
}

// TestResolveRevsArgv pins the pure resolver argv: a one-shot run with the
// outDir bind-mounted at /out, and one `git ls-remote | cut > /out/rev.<name>`
// per input at its documented ref.
func TestResolveRevsArgv(t *testing.T) {
	got := resolveRevsArgv("docker.io/nixos/nix:test", "/tmp/out", pinInputs())
	s := strings.Join(got, " ")
	for _, want := range []string{
		"run --rm",
		"--volume /tmp/out:/out:rw",
		"docker.io/nixos/nix:test sh -lc",
		"git ls-remote https://github.com/NixOS/nixpkgs refs/heads/nixos-unstable | cut -f1 > /out/rev.nixpkgs",
		"git ls-remote https://github.com/numtide/llm-agents.nix HEAD | cut -f1 > /out/rev.llm-agents",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("resolveRevsArgv missing %q in:\n%s", want, s)
		}
	}
	if got[len(got)-4] != "docker.io/nixos/nix:test" {
		t.Errorf("builder image must come right before sh -lc: %v", got)
	}
	if !strings.HasPrefix(got[len(got)-1], "set -e") {
		t.Errorf("resolver script must fail-closed with set -e: %q", got[len(got)-1])
	}
}

// writePinEngine writes a fake engine that finds the bind-mounted out dir in
// its argv (the *:/out:rw volume — the same real-temp-path trick as
// writeFakeEngine) and writes the given revs as the resolver would.
func writePinEngine(t *testing.T, nixpkgsRev, llmAgentsRev string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "engine")
	script := "#!/bin/sh\n" +
		"out=\"\"\n" +
		"for a in \"$@\"; do case \"$a\" in *:/out:rw) out=\"${a%%:*}\";; esac; done\n" +
		"[ -n \"$out\" ] || exit 1\n"
	if nixpkgsRev != "" {
		script += "echo '" + nixpkgsRev + "' > \"$out/rev.nixpkgs\"\n"
	}
	if llmAgentsRev != "" {
		script += "echo '" + llmAgentsRev + "' > \"$out/rev.llm-agents\"\n"
	}
	script += "exit 0\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestResolveLatest drives the resolver with fake engines: a clean resolve
// returns 40-hex pins per input, and every failure mode (no engine, engine
// error, missing rev file, malformed rev) is fail-closed.
func TestResolveLatest(t *testing.T) {
	requireExec(t, "sh")
	revA, revB := strings.Repeat("a", 40), strings.Repeat("b", 40)

	pins, err := ResolveLatest(writePinEngine(t, revA, revB), "", &strings.Builder{})
	if err != nil {
		t.Fatalf("ResolveLatest: %v", err)
	}
	if pins["nixpkgs"].Rev != revA || pins["llm-agents"].Rev != revB {
		t.Errorf("pins = %+v, want revs %s / %s", pins, revA, revB)
	}
	if pins["nixpkgs"].Ref != "refs/heads/nixos-unstable" || pins["llm-agents"].Ref != "HEAD" {
		t.Errorf("pins must record the resolved refs: %+v", pins)
	}

	if _, err := ResolveLatest("", "", nil); err == nil {
		t.Error("no engine must error")
	}
	fail := filepath.Join(t.TempDir(), "engine-fail")
	if err := os.WriteFile(fail, []byte("#!/bin/sh\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveLatest(fail, "", nil); err == nil {
		t.Error("a failing resolver run must error")
	}
	// Engine succeeds but writes only one rev file → the missing one errors.
	if _, err := ResolveLatest(writePinEngine(t, revA, ""), "", nil); err == nil ||
		!strings.Contains(err.Error(), "llm-agents") {
		t.Errorf("missing rev file = %v, want an llm-agents error", err)
	}
	// A non-40-hex rev (e.g. an HTML error page) is rejected.
	if _, err := ResolveLatest(writePinEngine(t, "not-a-rev", revB), "", nil); err == nil ||
		!strings.Contains(err.Error(), "40-hex") {
		t.Errorf("malformed rev = %v, want a 40-hex validation error", err)
	}
}

// TestPinSpec is the resolution table: pass-throughs never touch the cache, a
// warm stamp needs no engine, a miss resolves+stamps once, refresh re-resolves
// over a warm stamp, and no-engine-on-a-cold-cache errors with build-image
// guidance.
func TestPinSpec(t *testing.T) {
	requireExec(t, "sh")
	revA, revB := strings.Repeat("a", 40), strings.Repeat("b", 40)

	// Concrete and empty revs pass through untouched — even with refresh, no
	// engine and no cache at all.
	pinsCacheDir(t)
	for _, s := range []Spec{
		{},
		{Attrs: []string{"go"}},
		{Attrs: []string{"go"}, NixpkgsRev: "1234abcd", LLMAgentsRev: revB},
	} {
		got, err := PinSpec(s, "", "", true, nil)
		if err != nil || got.NixpkgsRev != s.NixpkgsRev || got.LLMAgentsRev != s.LLMAgentsRev {
			t.Errorf("PinSpec(%+v) = %+v, %v; want untouched pass-through", s, got, err)
		}
	}
	if path, _ := PinsPath(); fileExistsForTest(path) {
		t.Error("a pass-through must not stamp the pins cache")
	}

	// Cold cache + no engine → fail-closed with build-image guidance.
	if _, err := PinSpec(Spec{NixpkgsRev: "latest"}, "", "", false, nil); err == nil ||
		!strings.Contains(err.Error(), "image build") {
		t.Errorf("cold cache without an engine = %v, want build-image guidance", err)
	}

	// Miss → ResolveLatest via the engine, both revs replaced, cache stamped.
	got, err := PinSpec(Spec{NixpkgsRev: "latest", LLMAgentsRev: "latest"},
		writePinEngine(t, revA, revB), "", false, &strings.Builder{})
	if err != nil {
		t.Fatalf("PinSpec miss: %v", err)
	}
	if got.NixpkgsRev != revA || got.LLMAgentsRev != revB {
		t.Errorf("resolved spec = %+v, want %s / %s", got, revA, revB)
	}
	stamped, err := LoadPins()
	if err != nil || stamped["nixpkgs"].Rev != revA || stamped["llm-agents"].Rev != revB {
		t.Errorf("stamped pins = %+v, %v", stamped, err)
	}

	// Warm cache hit: no engine needed (the dry-run case), revs from the stamp.
	hit, err := PinSpec(Spec{LLMAgentsRev: "latest"}, "", "", false, nil)
	if err != nil || hit.LLMAgentsRev != revB {
		t.Errorf("warm-cache hit = %+v, %v; want the stamped rev with no engine", hit, err)
	}

	// Refresh forces a re-resolve over the warm stamp and re-stamps.
	revC := strings.Repeat("c", 40)
	ref, err := PinSpec(Spec{NixpkgsRev: "latest"}, writePinEngine(t, revC, revC), "", true, nil)
	if err != nil || ref.NixpkgsRev != revC {
		t.Errorf("refresh = %+v, %v; want the re-resolved rev", ref, err)
	}
	if restamped, _ := LoadPins(); restamped["nixpkgs"].Rev != revC {
		t.Errorf("refresh must move the stamp, got %+v", restamped)
	}
	// Refresh with no engine is the same fail-closed error.
	if _, err := PinSpec(Spec{NixpkgsRev: "latest"}, "", "", true, nil); err == nil {
		t.Error("refresh without an engine must error")
	}

	// A corrupt pins file fails the pinning instead of re-resolving over it.
	path, _ := PinsPath()
	if err := os.WriteFile(path, []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PinSpec(Spec{NixpkgsRev: "latest"}, "", "", false, nil); err == nil {
		t.Error("a corrupt pins cache must error")
	}
}

// TestPinSpecMissKeepsOtherStamps: a cache miss stamps ONLY the missing
// inputs. Profile A's warm nixpkgs stamp must not move when profile B's first
// enter resolves a cold llm-agents pin — or A's next enter would mint a new
// tag and rebuild for no reason (only --refresh moves a warm stamp).
func TestPinSpecMissKeepsOtherStamps(t *testing.T) {
	requireExec(t, "sh")
	pinsCacheDir(t)
	revA, revC := strings.Repeat("a", 40), strings.Repeat("c", 40)
	if err := SavePins(Pins{"nixpkgs": {Ref: "refs/heads/nixos-unstable", Rev: revA}}); err != nil {
		t.Fatal(err)
	}

	// llm-agents is cold → resolve runs (returning revC for BOTH inputs), but
	// only the missing llm-agents stamp is written.
	got, err := PinSpec(Spec{LLMAgentsRev: "latest"}, writePinEngine(t, revC, revC), "", false, nil)
	if err != nil || got.LLMAgentsRev != revC {
		t.Fatalf("miss = %+v, %v; want llm-agents pinned to %s", got, err, revC)
	}
	pins, err := LoadPins()
	if err != nil {
		t.Fatal(err)
	}
	if pins["nixpkgs"].Rev != revA {
		t.Errorf("warm nixpkgs stamp moved to %s on an unrelated miss, want %s kept", pins["nixpkgs"].Rev, revA)
	}
	if pins["llm-agents"].Rev != revC {
		t.Errorf("llm-agents stamp = %s, want %s", pins["llm-agents"].Rev, revC)
	}

	// refresh is the deliberate full re-stamp: every resolved input moves.
	if _, err := PinSpec(Spec{LLMAgentsRev: "latest"}, writePinEngine(t, revC, revC), "", true, nil); err != nil {
		t.Fatal(err)
	}
	if re, _ := LoadPins(); re["nixpkgs"].Rev != revC {
		t.Errorf("refresh must move every stamp, nixpkgs = %s", re["nixpkgs"].Rev)
	}
}

// TestPinSpecResolveAndStampErrors: the two cold-cache failure branches are
// fail-closed — a failing resolver run and a stamp that cannot be written both
// error instead of guessing.
func TestPinSpecResolveAndStampErrors(t *testing.T) {
	requireExec(t, "sh")

	// Resolver exits non-zero → the ResolveLatest error propagates.
	pinsCacheDir(t)
	fail := filepath.Join(t.TempDir(), "engine-fail")
	if err := os.WriteFile(fail, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := PinSpec(Spec{NixpkgsRev: "latest"}, fail, "", false, nil); err == nil ||
		!strings.Contains(err.Error(), "resolve latest") {
		t.Errorf("failing resolver = %v, want a resolve error", err)
	}

	// Resolve succeeds but the stamp cannot be written (a file squats on the
	// cache dir) → SavePins' error propagates.
	cache := pinsCacheDir(t)
	if err := os.WriteFile(filepath.Join(cache, "sandboxer"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rev := strings.Repeat("a", 40)
	if _, err := PinSpec(Spec{NixpkgsRev: "latest"}, writePinEngine(t, rev, rev), "", false, nil); err == nil {
		t.Error("unwritable pins cache must fail the pinning")
	}
}

// TestPinSpecNixImageOverride: the resolver runs the caller's --nix-image
// builder, not the hardcoded default — an env that overrides the image because
// docker.io is unreachable must be able to resolve a latest pin too.
func TestPinSpecNixImageOverride(t *testing.T) {
	requireExec(t, "sh")
	pinsCacheDir(t)
	rev := strings.Repeat("a", 40)
	log := filepath.Join(t.TempDir(), "argv.log")
	eng := filepath.Join(t.TempDir(), "engine")
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> " + log + "\n" +
		"out=\"\"\n" +
		"for a in \"$@\"; do case \"$a\" in *:/out:rw) out=\"${a%%:*}\";; esac; done\n" +
		"[ -n \"$out\" ] && { echo " + rev + " > \"$out/rev.nixpkgs\"; echo " + rev + " > \"$out/rev.llm-agents\"; }\n" +
		"exit 0\n"
	if err := os.WriteFile(eng, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := PinSpec(Spec{NixpkgsRev: "latest"}, eng, "registry.example/nix:custom", false, nil); err != nil {
		t.Fatal(err)
	}
	if l := readFile(t, log); !strings.Contains(l, "registry.example/nix:custom") {
		t.Errorf("resolver must run the overridden builder image:\n%s", l)
	}
}

// TestPinSpecThenTag: the pinned spec tags cleanly (the Tag() latest-panic is
// unreachable after PinSpec) and the tag is content-addressed by the resolved
// rev — a moved pin yields a new image.
func TestPinSpecThenTag(t *testing.T) {
	requireExec(t, "sh")
	pinsCacheDir(t)
	revA := strings.Repeat("a", 40)
	s, err := PinSpec(Spec{Attrs: []string{"go"}, NixpkgsRev: "latest"},
		writePinEngine(t, revA, revA), "", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	tag := s.Tag()
	if !strings.HasPrefix(tag, "sandboxer-toolbox:var-") {
		t.Fatalf("pinned tag = %q", tag)
	}
	if tag == (Spec{Attrs: []string{"go"}}).Tag() {
		t.Error("a resolved latest rev must change the tag away from the embedded pin")
	}
}

func fileExistsForTest(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
