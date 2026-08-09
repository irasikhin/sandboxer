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

// writeFakeGit installs a `git` shim on a PATH that contains nothing else, so
// resolveRevsHostGit's `git ls-remote` is driven deterministically (no real git,
// no network). body is the shim's shell body; the shim always exits 0 unless the
// body exits otherwise.
func writeFakeGit(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" + body + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

// fakeGitRevs renders the body of a shim that answers `git ls-remote` for the
// two flake inputs with the given revs (sha per line, tab-separated, exactly
// like real git).
func fakeGitRevs(nixpkgsRev, llmAgentsRev string) string {
	return "case \"$2\" in\n" +
		"  *NixOS/nixpkgs) echo '" + nixpkgsRev + "\trefs/heads/nixos-unstable';;\n" +
		"  *llm-agents.nix) echo '" + llmAgentsRev + "\tHEAD';;\n" +
		"esac\n"
}

// TestResolveLatest drives the host-git resolver: a clean resolve returns
// 40-hex pins per input, refs are recorded, and every failure mode (git error,
// a non-40-hex answer, an empty answer) is fail-closed.
func TestResolveLatest(t *testing.T) {
	revA, revB := strings.Repeat("a", 40), strings.Repeat("b", 40)

	writeFakeGit(t, fakeGitRevs(revA, revB))
	pins, err := ResolveLatest(&strings.Builder{})
	if err != nil {
		t.Fatalf("ResolveLatest: %v", err)
	}
	if pins["nixpkgs"].Rev != revA || pins["llm-agents"].Rev != revB {
		t.Errorf("pins = %+v, want revs %s / %s", pins, revA, revB)
	}
	if pins["nixpkgs"].Ref != "refs/heads/nixos-unstable" || pins["llm-agents"].Ref != "HEAD" {
		t.Errorf("pins must record the resolved refs: %+v", pins)
	}

	// A failing git run errors.
	writeFakeGit(t, "exit 1")
	if _, err := ResolveLatest(nil); err == nil {
		t.Error("a failing git run must error")
	}
	// A non-40-hex rev (e.g. an HTML error page) is rejected.
	writeFakeGit(t, "case \"$2\" in *llm-agents.nix) echo 'not-a-rev\tHEAD';; esac")
	if _, err := ResolveLatest(nil); err == nil || !strings.Contains(err.Error(), "40-hex") {
		t.Errorf("malformed rev = %v, want a 40-hex validation error", err)
	}
	// A ref that matches nothing (empty answer) is rejected.
	writeFakeGit(t, "case \"$2\" in *NixOS/nixpkgs) :;; esac")
	if _, err := ResolveLatest(nil); err == nil || !strings.Contains(err.Error(), "nixpkgs") {
		t.Errorf("empty answer = %v, want a nixpkgs error", err)
	}
}

// TestPinSpec is the resolution table: fully pinned specs never touch the
// cache, a warm stamp needs no git at all, a miss resolves+stamps once via host
// git, and refresh re-resolves over a warm stamp. A cold cache resolves on a
// container-less host exactly as on one with docker/podman — there is no engine
// anywhere in this path.
func TestPinSpec(t *testing.T) {
	requireExec(t, "sh")
	revA, revB := strings.Repeat("a", 40), strings.Repeat("b", 40)

	// Concrete revs pass through untouched — even with refresh, no cache at all.
	pinsCacheDir(t)
	s := Spec{Attrs: []string{"go"}, NixpkgsRev: revA, LLMAgentsRev: revB}
	got, err := PinSpec(s, true, nil)
	if err != nil || got.NixpkgsRev != revA || got.LLMAgentsRev != revB {
		t.Errorf("PinSpec(%+v) = %+v, %v; want untouched pass-through", s, got, err)
	}
	if path, _ := PinsPath(); fileExistsForTest(path) {
		t.Error("a pass-through must not stamp the pins cache")
	}

	// Cold cache → resolve via host git (the phase-A acceptance: a first build
	// on a docker-less host works), both revs replaced, cache stamped. The
	// empty spec tracks both inputs — the stock auto-update default.
	writeFakeGit(t, fakeGitRevs(revA, revB))
	got, err = PinSpec(Spec{}, false, &strings.Builder{})
	if err != nil {
		t.Fatalf("PinSpec cold miss: %v", err)
	}
	if got.NixpkgsRev != revA || got.LLMAgentsRev != revB {
		t.Errorf("resolved spec = %+v, want %s / %s", got, revA, revB)
	}
	stamped, err := LoadPins()
	if err != nil || stamped["nixpkgs"].Rev != revA || stamped["llm-agents"].Rev != revB {
		t.Errorf("stamped pins = %+v, %v", stamped, err)
	}

	// Warm cache hit: no resolve at all (the dry-run case), revs from the stamp
	// for the explicit "latest" and the "" default alike.
	hit, err := PinSpec(Spec{LLMAgentsRev: "latest"}, false, nil)
	if err != nil || hit.LLMAgentsRev != revB || hit.NixpkgsRev != revA {
		t.Errorf("warm-cache hit = %+v, %v; want the stamped revs with no git", hit, err)
	}

	// Refresh forces a re-resolve over the warm stamp and re-stamps.
	revC := strings.Repeat("c", 40)
	writeFakeGit(t, fakeGitRevs(revC, revC))
	ref, err := PinSpec(Spec{NixpkgsRev: "latest"}, true, nil)
	if err != nil || ref.NixpkgsRev != revC {
		t.Errorf("refresh = %+v, %v; want the re-resolved rev", ref, err)
	}
	if restamped, _ := LoadPins(); restamped["nixpkgs"].Rev != revC {
		t.Errorf("refresh must move the stamp, got %+v", restamped)
	}

	// A corrupt pins file fails the pinning instead of re-resolving over it.
	path, _ := PinsPath()
	if err := os.WriteFile(path, []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := PinSpec(Spec{NixpkgsRev: "latest"}, false, nil); err == nil {
		t.Error("a corrupt pins cache must error")
	}
}

// TestPinSpecMissKeepsOtherStamps: a cache miss stamps ONLY the missing
// inputs. Profile A's warm nixpkgs stamp must not move when profile B's first
// enter resolves a cold llm-agents pin — or A's next enter would mint a new
// tag and rebuild for no reason (only image build's refresh moves a warm
// stamp).
func TestPinSpecMissKeepsOtherStamps(t *testing.T) {
	requireExec(t, "sh")
	pinsCacheDir(t)
	revA, revC := strings.Repeat("a", 40), strings.Repeat("c", 40)
	if err := SavePins(Pins{"nixpkgs": {Ref: "refs/heads/nixos-unstable", Rev: revA}}); err != nil {
		t.Fatal(err)
	}

	// llm-agents is cold → resolve runs (returning revC for BOTH inputs), but
	// only the missing llm-agents stamp is written.
	writeFakeGit(t, fakeGitRevs(revC, revC))
	got, err := PinSpec(Spec{LLMAgentsRev: "latest"}, false, nil)
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
	writeFakeGit(t, fakeGitRevs(revC, revC))
	if _, err := PinSpec(Spec{LLMAgentsRev: "latest"}, true, nil); err != nil {
		t.Fatal(err)
	}
	if re, _ := LoadPins(); re["nixpkgs"].Rev != revC {
		t.Errorf("refresh must move every stamp, nixpkgs = %s", re["nixpkgs"].Rev)
	}
}

// TestPinSpecResolveAndStampErrors: the two failure branches are fail-closed —
// a failing git run and a stamp that cannot be written both error instead of
// guessing.
func TestPinSpecResolveAndStampErrors(t *testing.T) {
	requireExec(t, "sh")

	// git exits non-zero → the ResolveLatest error propagates.
	pinsCacheDir(t)
	writeFakeGit(t, "exit 1")
	if _, err := PinSpec(Spec{NixpkgsRev: "latest"}, false, nil); err == nil ||
		!strings.Contains(err.Error(), "resolve latest") {
		t.Errorf("failing git = %v, want a resolve error", err)
	}

	// Resolve succeeds but the stamp cannot be written (a file squats on the
	// cache dir) → SavePins' error propagates.
	cache := pinsCacheDir(t)
	if err := os.WriteFile(filepath.Join(cache, "sandboxer"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rev := strings.Repeat("a", 40)
	writeFakeGit(t, fakeGitRevs(rev, rev))
	if _, err := PinSpec(Spec{NixpkgsRev: "latest"}, false, nil); err == nil {
		t.Error("unwritable pins cache must fail the pinning")
	}
}

// TestPinSpecThenTag: the pinned spec tags cleanly (the Tag() latest-panic is
// unreachable after PinSpec) and the tag is content-addressed by the resolved
// rev — a moved pin yields a new image.
func TestPinSpecThenTag(t *testing.T) {
	requireExec(t, "sh")
	pinsCacheDir(t)
	revA := strings.Repeat("a", 40)
	writeFakeGit(t, fakeGitRevs(revA, revA))
	s, err := PinSpec(Spec{Attrs: []string{"go"}, NixpkgsRev: "latest"}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	tag := s.Tag()
	if !strings.HasPrefix(tag, "sandboxer-toolbox:var-") {
		t.Fatalf("pinned tag = %q", tag)
	}
	if other := strings.Repeat("b", 40); tag == (Spec{Attrs: []string{"go"}, NixpkgsRev: other, LLMAgentsRev: other}).Tag() {
		t.Error("the tag must be content-addressed by the resolved revs")
	}
}

func fileExistsForTest(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
