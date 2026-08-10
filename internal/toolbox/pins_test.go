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
		"nixpkgs": {Ref: "refs/heads/nixos-unstable", Rev: strings.Repeat("a", 40), ResolvedAt: "2026-06-10T00:00:00Z"},
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
	if got["nixpkgs"] != want["nixpkgs"] {
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
// nixpkgs flake input with the given rev (sha + tab + ref, exactly like real
// git).
func fakeGitRevs(nixpkgsRev string) string {
	return "case \"$2\" in\n" +
		"  *NixOS/nixpkgs) echo '" + nixpkgsRev + "\trefs/heads/nixos-unstable';;\n" +
		"esac\n"
}

// TestResolveLatest drives the host-git resolver: a clean resolve returns
// 40-hex pins per input, refs are recorded, and every failure mode (git error,
// a non-40-hex answer, an empty answer) is fail-closed.
func TestResolveLatest(t *testing.T) {
	revA := strings.Repeat("a", 40)

	writeFakeGit(t, fakeGitRevs(revA))
	pins, err := ResolveLatest(&strings.Builder{})
	if err != nil {
		t.Fatalf("ResolveLatest: %v", err)
	}
	if pins["nixpkgs"].Rev != revA {
		t.Errorf("pins = %+v, want rev %s", pins, revA)
	}
	if pins["nixpkgs"].Ref != "refs/heads/nixos-unstable" {
		t.Errorf("pins must record the resolved ref: %+v", pins)
	}

	// A failing git run errors.
	writeFakeGit(t, "exit 1")
	if _, err := ResolveLatest(nil); err == nil {
		t.Error("a failing git run must error")
	}
	// A non-40-hex rev (e.g. an HTML error page) is rejected.
	writeFakeGit(t, "case \"$2\" in *NixOS/nixpkgs) echo 'not-a-rev\trefs/heads/nixos-unstable';; esac")
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
	revA := strings.Repeat("a", 40)

	// A concrete rev passes through untouched — even with refresh, no cache at all.
	pinsCacheDir(t)
	s := Spec{Attrs: []string{"go"}, NixpkgsRev: revA}
	got, err := PinSpec(s, true, nil)
	if err != nil || got.NixpkgsRev != revA {
		t.Errorf("PinSpec(%+v) = %+v, %v; want untouched pass-through", s, got, err)
	}
	if path, _ := PinsPath(); fileExistsForTest(path) {
		t.Error("a pass-through must not stamp the pins cache")
	}

	// Cold cache → resolve via host git (the phase-A acceptance: a first build
	// on an engine-less host works), the rev replaced, cache stamped. The
	// empty spec tracks the input — the stock auto-update default.
	writeFakeGit(t, fakeGitRevs(revA))
	got, err = PinSpec(Spec{}, false, &strings.Builder{})
	if err != nil {
		t.Fatalf("PinSpec cold miss: %v", err)
	}
	if got.NixpkgsRev != revA {
		t.Errorf("resolved spec = %+v, want %s", got, revA)
	}
	stamped, err := LoadPins()
	if err != nil || stamped["nixpkgs"].Rev != revA {
		t.Errorf("stamped pins = %+v, %v", stamped, err)
	}

	// Warm cache hit: no resolve at all (the dry-run case), the rev from the
	// stamp for the explicit "latest" and the "" default alike.
	writeFakeGit(t, "exit 1")
	hit, err := PinSpec(Spec{NixpkgsRev: "latest"}, false, nil)
	if err != nil || hit.NixpkgsRev != revA {
		t.Errorf("warm-cache hit = %+v, %v; want the stamped rev with no git", hit, err)
	}

	// Refresh forces a re-resolve over the warm stamp and re-stamps.
	revC := strings.Repeat("c", 40)
	writeFakeGit(t, fakeGitRevs(revC))
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
	writeFakeGit(t, fakeGitRevs(rev))
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
	writeFakeGit(t, fakeGitRevs(revA))
	s, err := PinSpec(Spec{Attrs: []string{"go"}, NixpkgsRev: "latest"}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	tag := s.Tag()
	if !strings.HasPrefix(tag, "sandboxer-toolbox:var-") {
		t.Fatalf("pinned tag = %q", tag)
	}
	if other := strings.Repeat("b", 40); tag == (Spec{Attrs: []string{"go"}, NixpkgsRev: other}).Tag() {
		t.Error("the tag must be content-addressed by the resolved rev")
	}
}

func fileExistsForTest(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
