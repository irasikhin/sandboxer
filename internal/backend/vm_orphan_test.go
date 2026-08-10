package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A microVM sweep that reads only the host-side records cannot see a machine
// whose record was lost — a wiped state dir, a changed SANDBOXER_STATE — even
// though it is still running and holding disk. Every other VM sweep iterates
// records, so that machine was invisible to clean, list AND doctor at once.
// These pin that vmOrphanSessions asks the engine too.

// startFakeMachine makes the fake runner report a running machine of that name,
// mirroring what `msb create` would have left behind. State is not a
// parameter: orphanhood is about attribution, not run state — a stopped machine
// with a lost record is just as invisible and just as much disk.
func startFakeMachine(t *testing.T, name string) {
	t.Helper()
	dir := os.Getenv("FAKE_MACHINES")
	if dir == "" {
		t.Fatal("FAKE_MACHINES unset — setupFakeMSB must run first")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("Running"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestVMOrphansIncludeUnrecordedMachines is the regression: a running,
// sandboxer-named machine with no record at all is an orphan.
func TestVMOrphansIncludeUnrecordedMachines(t *testing.T) {
	requireExec(t, "sh")
	setupFakeMSB(t)

	startFakeMachine(t, "sandboxer-lost-deadbeef")

	got, err := vmOrphanSessions()
	if err != nil {
		t.Fatalf("vmOrphanSessions: %v", err)
	}
	if len(got) != 1 || got[0] != "sandboxer-lost-deadbeef" {
		t.Errorf("orphans = %v, want the unrecorded machine — a lost record must not hide a live VM", got)
	}
}

// TestVMOrphansKeepRecordedMissingBaseDir keeps the original case working: a
// recorded machine whose project directory is gone.
func TestVMOrphansKeepRecordedMissingBaseDir(t *testing.T) {
	requireExec(t, "sh")
	setupFakeMSB(t)

	rec := vmRecord{Name: "sandboxer-gone-cafe0000", BaseDir: filepath.Join(t.TempDir(), "deleted"), Slug: "gone"}
	if err := writeVMRecord(rec); err != nil {
		t.Fatal(err)
	}
	startFakeMachine(t, rec.Name)

	got, err := vmOrphanSessions()
	if err != nil {
		t.Fatalf("vmOrphanSessions: %v", err)
	}
	// Reported once, not twice: it is recorded AND live, and the record branch
	// already claimed it.
	if len(got) != 1 || got[0] != rec.Name {
		t.Errorf("orphans = %v, want exactly [%s]", got, rec.Name)
	}
}

// TestVMOrphansIgnoreHealthyAndForeign is the false-positive guard: a machine
// whose project still exists is not an orphan, and a machine that is not ours
// is never reported — the name prefix is the only ownership evidence a
// recordless machine carries, so it has to be applied strictly.
func TestVMOrphansIgnoreHealthyAndForeign(t *testing.T) {
	requireExec(t, "sh")
	setupFakeMSB(t)

	live := t.TempDir() // the project is still there
	rec := vmRecord{Name: "sandboxer-ok-11112222", BaseDir: live, Slug: "ok"}
	if err := writeVMRecord(rec); err != nil {
		t.Fatal(err)
	}
	startFakeMachine(t, rec.Name)
	startFakeMachine(t, "someone-elses-vm")

	got, err := vmOrphanSessions()
	if err != nil {
		t.Fatalf("vmOrphanSessions: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("orphans = %v, want none (a live project and a foreign machine)", got)
	}
}

// TestVMOrphansSorted: doctor prints this list, so its order must not depend
// on directory iteration.
func TestVMOrphansSorted(t *testing.T) {
	requireExec(t, "sh")
	setupFakeMSB(t)

	for _, n := range []string{"sandboxer-zzz-00000000", "sandboxer-aaa-11111111", "sandboxer-mmm-22222222"} {
		startFakeMachine(t, n)
	}
	got, err := vmOrphanSessions()
	if err != nil {
		t.Fatalf("vmOrphanSessions: %v", err)
	}
	want := []string{"sandboxer-aaa-11111111", "sandboxer-mmm-22222222", "sandboxer-zzz-00000000"}
	if len(got) != len(want) {
		t.Fatalf("orphans = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("orphans = %v, want %v", got, want)
			break
		}
	}
}

// TestRemoveCommand pins the removal hint doctor prints. It must be a command
// the user can actually paste, in msb's own dialect — never a container-era
// `<engine> rm -f a b` spelling.
func TestRemoveCommand(t *testing.T) {
	if got := RemoveCommand(msbEngine, nil); got != "" {
		t.Errorf("no names should render no command, got %q", got)
	}
	got := RemoveCommand(msbEngine, []string{"a"})
	if !strings.HasSuffix(got, "remove -f a") {
		t.Errorf("%q should end with %q", got, "remove -f a")
	}
	if strings.Contains(got, " rm -f ") {
		t.Errorf("got the container spelling: %q", got)
	}
	// Several machines: one call each, not one call with many names.
	multi := RemoveCommand(msbEngine, []string{"a", "b"})
	if strings.Count(multi, "remove -f") != 2 {
		t.Errorf("msb removes one machine per call, got %q", multi)
	}
}
