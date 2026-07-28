package backend

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/toolbox"
)

// The microVM backends' failure and edge paths — the branches a happy-path
// lifecycle test never reaches, and where a silent wrong answer is expensive: a
// launch-time allowlist filter that drops the wrong entries, an image store with
// no state root, a preflight that lets a shadowed share through.

// TestVMResolvableDomains pins the smolvm launch filter: --allow-host resolves
// every entry at VM start and hard-fails the machine on any that does not, so
// unresolvable entries are dropped — but only those, order preserved, with the
// drop reported. Uses "localhost" (answered from the hosts file, so the test
// needs no network) against the reserved .invalid TLD, which can never resolve.
func TestVMResolvableDomains(t *testing.T) {
	var errb bytes.Buffer
	got := vmResolvableDomains([]string{"localhost", "nope.invalid", "  ", ".also-nope.invalid"}, &errb)
	if !slices.Equal(got, []string{"localhost"}) {
		t.Errorf("kept %q, want only the resolvable entry", got)
	}
	msg := errb.String()
	if !strings.Contains(msg, "dropped 3") || !strings.Contains(msg, "nope.invalid") {
		t.Errorf("warning did not name the dropped domains: %q", msg)
	}
	// A nil writer is a valid caller (no stderr wired) and must not panic.
	if got := vmResolvableDomains([]string{"nope.invalid"}, nil); got != nil {
		t.Errorf("got %q, want nothing kept", got)
	}
}

// TestVMCreatableDomains pins WHICH runner gets the filter: only smolvm, and
// only when an allowlist is actually in the launch argv. microsandbox rules are
// name-bound and matched at connect time, so filtering there would silently
// shrink a working allowlist.
func TestVMCreatableDomains(t *testing.T) {
	o := RunOpts{RT: config.Runtime{Egress: true, Domains: []string{"nope.invalid"}}}
	if got := vmCreatableDomains(o, msbRunner{}); !slices.Equal(got, o.RT.Domains) {
		t.Errorf("microsandbox domains were filtered: %q", got)
	}
	if got := vmCreatableDomains(o, smolvmRunner{}); len(got) != 0 {
		t.Errorf("smolvm kept an unresolvable domain: %q", got)
	}
	// A proxy takes the open-network path, which carries no per-host flag: the
	// list travels as env only, so nothing may be dropped from it.
	prox := o
	prox.RT.Proxy = "http://p:8080"
	if got := vmCreatableDomains(prox, smolvmRunner{}); !slices.Equal(got, o.RT.Domains) {
		t.Errorf("the proxy path filtered the allowlist: %q", got)
	}
	off := o
	off.RT.Egress = false
	if got := vmCreatableDomains(off, smolvmRunner{}); !slices.Equal(got, o.RT.Domains) {
		t.Errorf("egress off filtered the allowlist: %q", got)
	}
}

// TestMSBPreflightRejectsShadowedShares pins that every share the guest's /tmp
// tmpfs would hide is named — the sandbox root, the home, a source mount and a
// profile's extraMounts target — and that a sane layout passes.
func TestMSBPreflightRejectsShadowedShares(t *testing.T) {
	ok := RunOpts{Dest: "/var/tmp/p/sandboxes/box", HomeDir: "/home/dev/.state/home"}
	if err := msbPreflight(ok); err != nil {
		t.Errorf("a sandbox outside /tmp was refused: %v", err)
	}
	if err := (msbRunner{}).preflight(ok); err != nil {
		t.Errorf("runner preflight disagreed with msbPreflight: %v", err)
	}
	if err := (smolvmRunner{}).preflight(RunOpts{Dest: "/tmp/box"}); err != nil {
		t.Errorf("smolvm has no such trap and must not refuse: %v", err)
	}

	bad := RunOpts{
		Dest: "/tmp/box", HomeDir: "/tmp/home", SrcMounts: []string{"/tmp/src"},
		Profile: &config.Profile{ExtraMounts: []config.Mount{{Source: "/data", Target: "/tmp/data"}}},
	}
	err := msbPreflight(bad)
	if err == nil {
		t.Fatal("a share under /tmp was accepted — the guest tmpfs would hide it")
	}
	for _, want := range []string{"/tmp/box", "/tmp/home", "/tmp/src", "/tmp/data", "tmpfs"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// TestMSBExtraMountsAndEnv pins the profile passthrough in msb's dialect: ro is
// spelled on the volume, rw is the default and left off, and env keys are sorted
// so map order never leaks into the argv (and therefore into the session hash).
func TestMSBExtraMountsAndEnv(t *testing.T) {
	p := &config.Profile{
		ExtraMounts: []config.Mount{
			{Source: "/data", Target: "/data", Mode: "ro"},
			{Source: "/cache", Target: "/cache"},
		},
		Env: map[string]string{"ZZ": "last", "AA": "first"},
	}
	want := []string{
		"-v", "/data:/data:ro", "-v", "/cache:/cache",
		"-e", "AA=first", "-e", "ZZ=last",
	}
	if got := msbExtraMountsAndEnv(p); !slices.Equal(got, want) {
		t.Errorf("msbExtraMountsAndEnv =\n%q\nwant\n%q", got, want)
	}
	if got := msbExtraMountsAndEnv(nil); got != nil {
		t.Errorf("no profile = no flags, got %q", got)
	}
}

// TestVMImageStoreWithoutStateRoot pins the no-state-root stance: the store
// degrades to "there is no image" instead of writing to a guessed path, and a
// write says so rather than failing silently.
func TestVMImageStoreWithoutStateRoot(t *testing.T) {
	t.Setenv("SANDBOXER_STATE", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "")
	if config.StateRoot() != "" {
		t.Skip("this host still resolves a state root")
	}
	if vmImagesDir() != "" || vmImagePath("img:1") != "" || msbLoadMarkerPath("img:1") != "" {
		t.Error("the image store resolved a path with no state root")
	}
	if vmImageExists("img:1") || vmImageID("img:1") != "" {
		t.Error("an image reads as present with no state root")
	}
	if err := vmStoreImage("img:1", "/nonexistent.tar"); err == nil {
		t.Error("storing an image with no state root must error")
	}
	if err := vmRemoveImage("img:1"); err != nil {
		t.Errorf("removing with no state root must be a no-op, got %v", err)
	}
}

// TestMSBImageCurrentWithoutTar pins the "keep what msb has" rule: with no build
// tar to compare against there is nothing to be stale relative to, and evicting
// a working cached image because its build artifact was reclaimed would be
// strictly worse.
func TestMSBImageCurrentWithoutTar(t *testing.T) {
	t.Setenv("SANDBOXER_STATE", t.TempDir())
	if !msbImageCurrent("img:1") {
		t.Error("with no tar in the store the cached image must count as current")
	}
	if err := os.MkdirAll(vmImagesDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vmImagePath("img:1"), []byte("tar"), 0o600); err != nil {
		t.Fatal(err)
	}
	if msbImageCurrent("img:1") {
		t.Error("a tar with no load marker must read as not-yet-imported")
	}
}

// TestMSBRemoveImageDropsCached pins the branch where msb HAS the image: it is
// removed from the runner's store as well as from the tar store.
func TestMSBRemoveImageDropsCached(t *testing.T) {
	log := setupFakeMSB(t)
	restore := msbImageInspect
	msbImageInspect = func(string) string { return "deadbeef" }
	t.Cleanup(func() { msbImageInspect = restore })

	if err := RemoveImage(msbEngine, "img:1"); err != nil {
		t.Fatalf("RemoveImage: %v", err)
	}
	if !strings.Contains(readFile(t, log), "image remove -f img:1") {
		t.Errorf("the cached image was not removed from msb's store:\n%s", readFile(t, log))
	}
}

// TestVMRunFailsClosed pins the one-shot path's refusals: a configuration the
// runner cannot honor (a share the guest would hide) and an image that cannot be
// resolved both exit non-zero with a diagnostic, never launching a machine.
func TestVMRunFailsClosed(t *testing.T) {
	setupFakeMSB(t)
	t.Setenv("SANDBOXER_NO_AUTOBUILD", "1")
	var errb bytes.Buffer
	o := RunOpts{
		Engine: msbEngine, Image: "img:1", Dest: "/tmp/box", Slug: "s",
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &errb,
	}
	if code := vmRun(o); code == 0 {
		t.Error("a share the guest tmpfs would hide must not run")
	}
	if !strings.Contains(errb.String(), "tmpfs") {
		t.Errorf("no explanation on stderr: %q", errb.String())
	}

	errb.Reset()
	noImage := o
	noImage.Dest = "/var/tmp/box"
	noImage.Image = config.DefaultImage // locally built, absent, autobuild off
	if code := vmRun(noImage); code == 0 {
		t.Error("a missing locally-built image must not run")
	}
	if !strings.Contains(errb.String(), "image build") {
		t.Errorf("no build hint on stderr: %q", errb.String())
	}
}

// TestVMCreateSessionAdoptsRaceWinner pins the create-race adoption: when create
// fails because a concurrent enter already made the machine, a running and
// hash-fresh machine is adopted instead of surfacing the name conflict.
func TestVMCreateSessionAdoptsRaceWinner(t *testing.T) {
	setupFakeMSB(t)
	base := t.TempDir()
	o := RunOpts{
		Engine: msbEngine, Image: "img:1", Dest: "/var/tmp/box", Slug: "s", BaseDir: base,
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
	}
	name := SessionName(o.Slug, o.BaseDir)
	hash := vmSessionWantHash(o)

	// The "winner": a live machine plus the record a successful create writes.
	if err := os.MkdirAll(os.Getenv("FAKE_MACHINES"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(os.Getenv("FAKE_MACHINES"), name), []byte("Running"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeVMRecord(msbRunner{}, vmRecord{Name: name, BaseDir: base, Slug: o.Slug, Hash: hash}); err != nil {
		t.Fatal(err)
	}
	// The fake CLI refuses a duplicate name exactly as the real one does, so
	// this create fails while the inventory still reports the winner.
	got, err := vmCreateSession(o, msbRunner{}, name, hash)
	if err != nil || got != name {
		t.Fatalf("vmCreateSession = %q, %v; want the adopted %q", got, err, name)
	}
}

// TestBuildVMImageRefusesWithoutBuilder pins the build fallback's error: with
// neither a container engine nor smolvm there is nothing to build the toolbox
// image WITH, and the user is told that instead of an exec failure for a binary
// they never asked for.
func TestBuildVMImageRefusesWithoutBuilder(t *testing.T) {
	t.Setenv("SANDBOXER_STATE", t.TempDir())
	t.Setenv("SANDBOXER_SMOLVM", "/nonexistent/smolvm-xyz")
	t.Setenv("SANDBOXER_ENGINE", "")
	t.Setenv("PATH", t.TempDir()) // no docker, no podman, no smolvm

	err := vmBuildImageToStore(RunOpts{Image: "img:1", Spec: toolbox.Spec{}})
	if err == nil || !strings.Contains(err.Error(), "smolvm") {
		t.Errorf("error = %v, want a no-builder-available explanation", err)
	}
}

// TestVMSizeConversions pins the size mapping both runners share, including the
// stances that keep a bad value from becoming a bad flag: an unparseable input
// falls back to the default, and a sub-unit value never rounds down to zero.
func TestVMSizeConversions(t *testing.T) {
	mem := map[string]string{
		"": vmDefaultMemMiB, "2G": "2048", "512M": "512", "1048576": "1",
		"2048K": "2", "nonsense": vmDefaultMemMiB, "1B": "1", "1k": "1",
	}
	for in, want := range mem {
		if got := vmMemMiB(in); got != want {
			t.Errorf("vmMemMiB(%q) = %q, want %q", in, got, want)
		}
	}
	cpus := map[string]string{
		"": vmDefaultCPUs, "2": "2", "1.5": "2", "150%": "2", "50%": "1",
		"nonsense": vmDefaultCPUs, "0": "1",
	}
	for in, want := range cpus {
		if got := vmCPUs(in); got != want {
			t.Errorf("vmCPUs(%q) = %q, want %q", in, got, want)
		}
	}
}
