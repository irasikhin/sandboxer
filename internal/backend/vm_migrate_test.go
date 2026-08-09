package backend

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/toolbox"
)

// fakeNix is a nix stand-in for the host-nix image-build routing: it prints a
// "store path" that points at a tar it drops (the shim writes the tar first),
// so BuildImageHostNix's orchestration (pins → nix build → store) runs without
// nix or a real build. /bin/sh is an absolute path, so it runs regardless of
// PATH (which the test empties of engines on purpose).
const fakeNix = `#!/bin/sh
printf 'IMAGE-TAR' > "$FAKE_TAR"
echo "$FAKE_TAR"
exit 0
`

// TestVMBuildImageToStoreHostNix pins the docker/podman-decommissioned image
// build: with a fake `nix` on PATH and warm pins (host-git resolution needs no
// engine), a microsandbox run realizes the toolbox image with HOST NIX — no
// container engine, no builder guest — and stores the tar. This is the seam
// that lets `image build --backend microsandbox` and the first enter auto-build
// work on a host that removed docker/podman.
func TestVMBuildImageToStoreHostNix(t *testing.T) {
	t.Setenv("SANDBOXER_STATE", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := toolbox.SavePins(toolbox.Pins{
		"nixpkgs":    {Ref: "refs/heads/nixos-unstable", Rev: strings.Repeat("d", 40)},
		"llm-agents": {Ref: "HEAD", Rev: strings.Repeat("e", 40)},
	}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nix"), []byte(fakeNix), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_TAR", filepath.Join(dir, "built.tar"))
	// A PATH with no container engine forces the host-nix path; the fake nix is
	// reached by name on this same PATH.
	t.Setenv("PATH", dir)

	image := config.DefaultImage
	if err := vmBuildImageToStore(RunOpts{Engine: msbEngine, Image: image, Stderr: &bytes.Buffer{}}); err != nil {
		t.Fatalf("vmBuildImageToStore via host nix: %v", err)
	}
	if !vmImageExists(image) {
		t.Error("image tar not stored after the host-nix build")
	}
}

// TestVMBuildImageToStoreNoNix pins the routing's failure mode when nothing can
// build the image: no nix on PATH. The error must name nix with an install
// hint — never a bare exec error from a binary the user never asked for.
func TestVMBuildImageToStoreNoNix(t *testing.T) {
	t.Setenv("SANDBOXER_STATE", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	if err := toolbox.SavePins(toolbox.Pins{
		"nixpkgs":    {Rev: strings.Repeat("d", 40)},
		"llm-agents": {Rev: strings.Repeat("e", 40)},
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())

	err := vmBuildImageToStore(RunOpts{Engine: msbEngine, Image: "img:1", Stderr: &bytes.Buffer{}})
	if err == nil || !strings.Contains(err.Error(), "nix is not installed") {
		t.Errorf("no-nix error = %v, want the nix install hint", err)
	}
}

// TestVMSharePreflightFileMount pins the file-extraMount guard shared by both
// microVM runners: virtio-fs shares directories only, so a profile extraMount
// whose source is a REGULAR FILE must be rejected up front with the reason,
// and a directory mount must keep working. msb's own /tmp trap still holds.
func TestVMSharePreflightFileMount(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "dotfile")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	mount := func(src string) RunOpts {
		return RunOpts{Profile: &config.Profile{ExtraMounts: []config.Mount{{Source: src, Target: "/etc/x"}}}}
	}

	if err := vmSharePreflight(mount(file)); err == nil || !strings.Contains(err.Error(), "directories only") {
		t.Errorf("file extraMount = %v, want the directories-only rejection", err)
	}
	if err := vmSharePreflight(mount(dir)); err != nil {
		t.Errorf("directory extraMount rejected: %v", err)
	}
	if err := vmSharePreflight(RunOpts{}); err != nil {
		t.Errorf("no-profile preflight: %v", err)
	}
	// A missing source is left to the runner (it materializes the path), never
	// rejected as a file.
	if err := vmSharePreflight(mount(filepath.Join(dir, "absent"))); err != nil {
		t.Errorf("absent extraMount rejected: %v", err)
	}

	// Both runners surface the guard through their own preflight.
	if err := (smolvmRunner{}).preflight(mount(file)); err == nil {
		t.Error("smolvm preflight accepted a file extraMount")
	}
	if err := (msbRunner{}).preflight(mount(file)); err == nil {
		t.Error("msb preflight accepted a file extraMount")
	}
	// msb's own /tmp shadowing is still rejected.
	if err := (msbRunner{}).preflight(RunOpts{Dest: "/tmp/x"}); err == nil || !strings.Contains(err.Error(), "tmpfs") {
		t.Errorf("msb /tmp preflight = %v, want the tmpfs rejection", err)
	}
}

// TestVMLimitsPreflight pins the phase-B limit validation: the microVM runners
// take a WHOLE number of vCPUs and a PARSEABLE memory cap, so a fractional
// limits.cpus or an unparseable limits.memory is a clear error, never the silent
// rounding / 4 GiB fallback the conversions used to apply.
func TestVMLimitsPreflight(t *testing.T) {
	if err := vmLimitsPreflight(RunOpts{CPU: "2", Mem: "2G"}); err != nil {
		t.Errorf("valid limits rejected: %v", err)
	}
	if err := vmLimitsPreflight(RunOpts{}); err != nil {
		t.Errorf("empty limits rejected: %v", err)
	}
	for _, bad := range []RunOpts{
		{CPU: "1.5"},
		{CPU: "150%"}, // a systemd quota of 1.5 cores is still not a whole count
		{CPU: "nonsense"},
	} {
		if err := vmLimitsPreflight(bad); err == nil || !strings.Contains(err.Error(), "limits.cpus") {
			t.Errorf("limits.cpus %q = %v, want a limits.cpus error", bad.CPU, err)
		}
	}
	if err := vmLimitsPreflight(RunOpts{Mem: "nonsense"}); err == nil || !strings.Contains(err.Error(), "limits.memory") {
		t.Errorf("bad memory = %v, want a limits.memory error", err)
	}
	// Both runners surface it through their own preflight.
	if err := (smolvmRunner{}).preflight(RunOpts{CPU: "1.5"}); err == nil {
		t.Error("smolvm preflight accepted a fractional limits.cpus")
	}
	if err := (msbRunner{}).preflight(RunOpts{Mem: "nope"}); err == nil {
		t.Error("msb preflight accepted an unparseable limits.memory")
	}
}

// TestVMRemoveAllSessionsReapsRecordless pins the phase-B sweep fix: a machine
// whose host-side record was lost (a wiped state dir, a changed state root) is
// still reaped by clean when its SessionName binds it to the project — and a
// recordless machine of a DIFFERENT base is left alone, so one project's clean
// never reaches another's leftovers.
func TestVMRemoveAllSessionsReapsRecordless(t *testing.T) {
	setupFakeSmolvm(t)
	base := t.TempDir()
	other := t.TempDir()
	if err := os.MkdirAll(os.Getenv("FAKE_MACHINES"), 0o700); err != nil {
		t.Fatal(err)
	}
	mkLive := func(n string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(os.Getenv("FAKE_MACHINES"), n), []byte("running"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Recorded machine for base (the classic path).
	if err := writeVMRecord(smolvmRunner{}, vmRecord{Name: "recorded", BaseDir: base, Slug: "s", Hash: "h"}); err != nil {
		t.Fatal(err)
	}
	mkLive("recorded")
	// RECORDLESS live machines: one bound to base by its SessionName hash, one
	// bound to a different base.
	bound, foreign := SessionName("s", base), SessionName("s", other)
	mkLive(bound)
	mkLive(foreign)

	if err := RemoveAllSessions(smolvmEngine, base); err != nil {
		t.Fatalf("RemoveAllSessions: %v", err)
	}
	for _, n := range []string{"recorded", bound} {
		if _, err := os.Stat(filepath.Join(os.Getenv("FAKE_MACHINES"), n)); err == nil {
			t.Errorf("machine %q survived clean", n)
		}
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("FAKE_MACHINES"), foreign)); err != nil {
		t.Errorf("another base's recordless machine was swept: %v", err)
	}
}

// TestVMAllSessionStatesSurfacesUnrecorded pins the phase-B listing fix: a live
// machine whose record was lost shows up in the host-wide view under the
// synthetic "(unrecorded)" bucket instead of being invisible, while recorded
// machines stay grouped by their real base.
func TestVMAllSessionStatesSurfacesUnrecorded(t *testing.T) {
	t.Setenv("SANDBOXER_STATE", t.TempDir())
	base := t.TempDir()
	if err := writeVMRecord(smolvmRunner{}, vmRecord{Name: "recorded", BaseDir: base, Slug: "s", Hash: "h"}); err != nil {
		t.Fatal(err)
	}
	restore := vmListMachines
	vmListMachines = func() []vmMachine {
		return []vmMachine{
			{Name: "recorded", State: "running"},
			{Name: SessionName("s", base), State: "running"}, // recordless
		}
	}
	t.Cleanup(func() { vmListMachines = restore })

	all, err := AllSessionStates(smolvmEngine)
	if err != nil {
		t.Fatal(err)
	}
	if all[base]["s"] != "running" {
		t.Errorf("recorded machine state = %v, want running under its base", all[base])
	}
	if all["(unrecorded)"][SessionName("s", base)] != "running" {
		t.Errorf("recordless machine not surfaced: %v", all)
	}
}
