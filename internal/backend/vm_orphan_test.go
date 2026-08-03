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
// records, so that machine was invisible to clean, list AND doctor at once. The
// container path has no such blind spot: it asks the engine by label. These pin
// that vmOrphanSessions asks the engine too.

// startFakeMachine makes the fake runner report a running machine of that name,
// mirroring what `machine create` would have left behind. State is not a
// parameter: orphanhood is about attribution, not run state — a stopped machine
// with a lost record is just as invisible and just as much disk.
func startFakeMachine(t *testing.T, name string) {
	t.Helper()
	dir := os.Getenv("FAKE_MACHINES")
	if dir == "" {
		t.Fatal("FAKE_MACHINES unset — setupFakeSmolvm must run first")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("running"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestVMOrphansIncludeUnrecordedMachines is the regression: a running,
// sandboxer-named machine with no record at all is an orphan.
func TestVMOrphansIncludeUnrecordedMachines(t *testing.T) {
	requireExec(t, "sh")
	setupFakeSmolvm(t)

	startFakeMachine(t, "sandboxer-lost-deadbeef")

	got, err := vmOrphanSessions(smolvmEngine)
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
	setupFakeSmolvm(t)

	rec := vmRecord{Name: "sandboxer-gone-cafe0000", BaseDir: filepath.Join(t.TempDir(), "deleted"), Slug: "gone"}
	if err := writeVMRecord(smolvmRunner{}, rec); err != nil {
		t.Fatal(err)
	}
	startFakeMachine(t, rec.Name)

	got, err := vmOrphanSessions(smolvmEngine)
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
// is never reported — the name prefix is the only ownership evidence a smolvm
// machine carries, so it has to be applied strictly.
func TestVMOrphansIgnoreHealthyAndForeign(t *testing.T) {
	requireExec(t, "sh")
	setupFakeSmolvm(t)

	live := t.TempDir() // the project is still there
	rec := vmRecord{Name: "sandboxer-ok-11112222", BaseDir: live, Slug: "ok"}
	if err := writeVMRecord(smolvmRunner{}, rec); err != nil {
		t.Fatal(err)
	}
	startFakeMachine(t, rec.Name)
	startFakeMachine(t, "someone-elses-vm")

	got, err := vmOrphanSessions(smolvmEngine)
	if err != nil {
		t.Fatalf("vmOrphanSessions: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("orphans = %v, want none (a live project and a foreign machine)", got)
	}
}

// TestVMOrphansSortedAndDeduped: doctor prints this list, so its order must not
// depend on directory iteration.
func TestVMOrphansSorted(t *testing.T) {
	requireExec(t, "sh")
	setupFakeSmolvm(t)

	for _, n := range []string{"sandboxer-zzz-00000000", "sandboxer-aaa-11111111", "sandboxer-mmm-22222222"} {
		startFakeMachine(t, n)
	}
	got, err := vmOrphanSessions(smolvmEngine)
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

// TestRemoveCommandPerEngine pins the removal hint doctor prints. It must be a
// command the user can actually paste: the container spelling (`<engine> rm -f
// a b`) is not a smolvm or microsandbox command at all, and nothing caught that
// while microVM orphans were invisible.
func TestRemoveCommandPerEngine(t *testing.T) {
	if got := RemoveCommand("docker", nil); got != "" {
		t.Errorf("no names should render no command, got %q", got)
	}
	if got := RemoveCommand("docker", []string{"a", "b"}); got != "docker rm -f a b" {
		t.Errorf("docker: %q", got)
	}
	for _, tc := range []struct{ engine, want string }{
		{smolvmEngine, "machine delete --name a -f"},
		{msbEngine, "remove -f a"},
	} {
		got := RemoveCommand(tc.engine, []string{"a"})
		if !strings.HasSuffix(got, tc.want) {
			t.Errorf("%s: %q should end with %q", tc.engine, got, tc.want)
		}
		if strings.Contains(got, " rm -f ") {
			t.Errorf("%s got the container spelling: %q", tc.engine, got)
		}
	}
	// Several machines: one call each, not one call with many names.
	multi := RemoveCommand(smolvmEngine, []string{"a", "b"})
	if strings.Count(multi, "machine delete") != 2 {
		t.Errorf("smolvm removes one machine per call, got %q", multi)
	}
}
