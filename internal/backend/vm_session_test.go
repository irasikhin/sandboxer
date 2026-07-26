package backend

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSmolvm is a bash stand-in for the smolvm CLI: it logs every invocation,
// keeps one file per machine (content = state) so `machine ls --json` reflects
// create/start/stop/delete, and for `machine exec` runs the command after `--`
// so exit codes propagate. It lets the lifecycle be driven end to end without a
// hypervisor, verifying the real argv wiring at the same time.
const fakeSmolvm = `#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = "--version" ]; then echo "smolvm 9.9.9-fake"; exit 0; fi
printf '%s\n' "$*" >> "$FAKE_LOG"
[ "${1:-}" = machine ] || { echo "unexpected $*" >&2; exit 2; }
cmd="$2"; shift 2
D="$FAKE_MACHINES"; mkdir -p "$D"
name=""; a=("$@")
for ((i=0; i<${#a[@]}; i++)); do
  if [ "${a[i]}" = "--name" ]; then name="${a[i+1]}"; fi
done
case "$cmd" in
  ls)
    out="["; first=1
    for f in "$D"/*; do
      [ -e "$f" ] || continue
      [ $first -eq 1 ] || out+=","
      first=0
      out+="{\"name\":\"$(basename "$f")\",\"state\":\"$(cat "$f")\"}"
    done
    printf '%s]\n' "$out"
    ;;
  create) printf stopped > "$D/$name" ;;
  start)  printf running > "$D/$name" ;;
  stop)   printf stopped > "$D/$name" ;;
  delete) rm -f "$D/$name" ;;
  exec|run)
    argv=(); seen=0
    for x in "$@"; do
      if [ "$seen" = 1 ]; then argv+=("$x"); fi
      if [ "$x" = "--" ]; then seen=1; fi
    done
    "${argv[@]}"
    ;;
  *) echo "unknown $cmd" >&2; exit 3 ;;
esac
`

// setupFakeSmolvm installs the fake CLI and points the backend at it, returning
// the log path so a test can assert which subcommands ran.
func setupFakeSmolvm(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "smolvm")
	if err := os.WriteFile(bin, []byte(fakeSmolvm), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "log")
	t.Setenv("SANDBOXER_SMOLVM", bin)
	t.Setenv("SANDBOXER_STATE", filepath.Join(dir, "state"))
	t.Setenv("FAKE_LOG", log)
	t.Setenv("FAKE_MACHINES", filepath.Join(dir, "machines-live"))
	return log
}

// TestVMSessionLifecycle drives create → exec → re-ensure (reuse) → recreate →
// stop → remove through the fake CLI, pinning the planSession dispatch, the
// record store and the exit-code propagation.
func TestVMSessionLifecycle(t *testing.T) {
	log := setupFakeSmolvm(t)
	base := t.TempDir()
	o := RunOpts{
		Engine: smolvmEngine, MountDest: true, Image: "img:1", Dest: "/d",
		Slug: "s", BaseDir: base, Stderr: &bytes.Buffer{},
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{},
	}
	name := SessionName("s", base)

	// 1. First ensure: no machine → create + start + record.
	got, err := EnsureSession(o)
	if err != nil || got != name {
		t.Fatalf("EnsureSession create = %q, %v; want %q", got, err, name)
	}
	if rec := readVMRecord(name); rec.Hash != vmSessionWantHash(o) {
		t.Errorf("record hash = %q, want %q", rec.Hash, vmSessionWantHash(o))
	}
	if info := InspectSession(smolvmEngine, name); !info.Running {
		t.Error("machine not running after create")
	}

	// 2. Re-ensure with the same config: fresh + running → exec, no new create.
	logBefore := readFile(t, log)
	if got, err := EnsureSession(o); err != nil || got != name {
		t.Fatalf("EnsureSession reuse = %q, %v", got, err)
	}
	if strings.Count(readFile(t, log), "machine create") != strings.Count(logBefore, "machine create") {
		t.Error("a fresh running session was recreated instead of reused")
	}

	// 3. Exec propagates exit codes.
	if code, _ := ExecSession(o, name, []string{"sh", "-c", "exit 7"}); code != 7 {
		t.Errorf("ExecSession exit = %d, want 7", code)
	}
	if code, _ := ExecSession(o, name, []string{"true"}); code != 0 {
		t.Errorf("ExecSession exit = %d, want 0", code)
	}

	// 4. Change the config → stale → recreate (delete + create).
	o2 := o
	o2.Mem = "2G" // different --mem → different want-hash
	if got, err := EnsureSession(o2); err != nil || got != name {
		t.Fatalf("EnsureSession recreate = %q, %v", got, err)
	}
	if !strings.Contains(readFile(t, log), "machine delete") {
		t.Error("stale session was not recreated (no delete)")
	}
	if rec := readVMRecord(name); rec.Hash != vmSessionWantHash(o2) {
		t.Errorf("record not updated after recreate: %q", rec.Hash)
	}

	// 5. States lists the slug; stop moves it to stopped.
	if err := StopSession(smolvmEngine, "s", base); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	states, err := SessionStates(smolvmEngine, base)
	if err != nil || states["s"] != "stopped" {
		t.Errorf("SessionStates = %v, %v; want s=stopped", states, err)
	}

	// 6. Remove deletes the machine and its record.
	if err := RemoveSession(smolvmEngine, "s", base); err != nil {
		t.Fatalf("RemoveSession: %v", err)
	}
	if info := InspectSession(smolvmEngine, name); info.Exists {
		t.Error("machine still exists after remove")
	}
	if rec := readVMRecord(name); rec.Name != "" {
		t.Error("record survived remove")
	}
}

// TestVMSessionStatesAndOrphans pins the record-vs-live cross reference: a
// recorded machine the engine forgot reads as "gone", and a record whose base
// dir vanished is an orphan.
func TestVMSessionStatesAndOrphans(t *testing.T) {
	t.Setenv("SANDBOXER_STATE", t.TempDir())
	base := t.TempDir()
	goneBase := filepath.Join(t.TempDir(), "deleted")

	writeRec := func(name, b, slug string) {
		if err := writeVMRecord(vmRecord{Name: name, BaseDir: b, Slug: slug, Hash: "h"}); err != nil {
			t.Fatal(err)
		}
	}
	writeRec("m-live", base, "live")
	writeRec("m-forgotten", base, "forgotten")
	writeRec("m-orphan", goneBase, "orphan")

	// Only m-live is in the live inventory.
	restore := vmListMachines
	vmListMachines = func() []vmMachine { return []vmMachine{{Name: "m-live", State: "running"}} }
	t.Cleanup(func() { vmListMachines = restore })

	states, err := SessionStates(smolvmEngine, base)
	if err != nil {
		t.Fatal(err)
	}
	if states["live"] != "running" {
		t.Errorf("live state = %q, want running", states["live"])
	}
	if states["forgotten"] != "gone" {
		t.Errorf("forgotten state = %q, want gone", states["forgotten"])
	}
	if _, ok := states["orphan"]; ok {
		t.Error("a different base's session leaked into states")
	}

	orphans, err := OrphanSessions(smolvmEngine)
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 1 || orphans[0] != "m-orphan" {
		t.Errorf("OrphanSessions = %v, want [m-orphan]", orphans)
	}
}

// TestSmolvmStatus pins the doctor helper: the fake CLI is found and reports a
// version, and a missing binary reads as not present.
func TestSmolvmStatus(t *testing.T) {
	setupFakeSmolvm(t)
	present, version, _ := SmolvmStatus()
	if !present || version != "smolvm 9.9.9-fake" {
		t.Errorf("SmolvmStatus() = present=%v version=%q, want true / smolvm 9.9.9-fake", present, version)
	}

	t.Setenv("SANDBOXER_SMOLVM", "/nonexistent/smolvm-xyz")
	if p, _, _ := SmolvmStatus(); p {
		t.Error("a missing smolvm must read as not present")
	}
}

// TestVMInspectAbsent pins the zero SessionInfo for a machine the engine does
// not know.
func TestVMInspectAbsent(t *testing.T) {
	restore := vmListMachines
	vmListMachines = func() []vmMachine { return nil }
	t.Cleanup(func() { vmListMachines = restore })
	if info := InspectSession(smolvmEngine, "nope"); info.Exists {
		t.Errorf("absent machine = %+v, want zero", info)
	}
}

// TestVMRunOneShot pins the one-shot ephemeral path (Run's smolvm dispatch),
// including exit-code propagation.
func TestVMRunOneShot(t *testing.T) {
	setupFakeSmolvm(t)
	o := RunOpts{
		Engine: smolvmEngine, Image: "img:1", Dest: "/d", Slug: "s",
		Args: []string{"sh", "-c", "exit 5"}, Stderr: &bytes.Buffer{},
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{},
	}
	code, err := Run(o)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if code != 5 {
		t.Errorf("Run exit = %d, want 5", code)
	}
}

// TestVMRemoveAllSessions pins the base-scoped sweep: only the given base's
// machines are removed, another base's record is left intact.
func TestVMRemoveAllSessions(t *testing.T) {
	setupFakeSmolvm(t)
	base := t.TempDir()
	other := t.TempDir()
	mkMachine := func(name, b, slug string) {
		if err := writeVMRecord(vmRecord{Name: name, BaseDir: b, Slug: slug, Hash: "h"}); err != nil {
			t.Fatal(err)
		}
		// Register it as live so the delete path runs.
		if err := os.WriteFile(filepath.Join(os.Getenv("FAKE_MACHINES"), name), []byte("running"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(os.Getenv("FAKE_MACHINES"), 0o700); err != nil {
		t.Fatal(err)
	}
	mkMachine("m-a", base, "a")
	mkMachine("m-b", base, "b")
	mkMachine("m-keep", other, "keep")

	if err := RemoveAllSessions(smolvmEngine, base); err != nil {
		t.Fatalf("RemoveAllSessions: %v", err)
	}
	if readVMRecord("m-a").Name != "" || readVMRecord("m-b").Name != "" {
		t.Error("base machines' records survived RemoveAllSessions")
	}
	if readVMRecord("m-keep").Name == "" {
		t.Error("another base's record was swept")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}
