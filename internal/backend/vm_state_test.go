package backend

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestVMRecordRoundTrip pins the per-machine record store: write, read back,
// list, remove — all under a temp state root.
func TestVMRecordRoundTrip(t *testing.T) {
	t.Setenv("SANDBOXER_STATE", t.TempDir())

	rec := vmRecord{Name: "sandboxer-s-deadbeef", BaseDir: "/b", Slug: "s", Hash: "h1", MountIDs: "m1"}
	if err := writeVMRecord(rec); err != nil {
		t.Fatalf("writeVMRecord: %v", err)
	}
	got := readVMRecord(rec.Name)
	if got != rec {
		t.Errorf("readVMRecord = %+v, want %+v", got, rec)
	}

	// A second machine, different base — both listed.
	rec2 := vmRecord{Name: "sandboxer-t-cafe0000", BaseDir: "/other", Slug: "t", Hash: "h2"}
	if err := writeVMRecord(rec2); err != nil {
		t.Fatalf("writeVMRecord 2: %v", err)
	}
	recs := listVMRecords()
	if len(recs) != 2 {
		t.Fatalf("listVMRecords = %d records, want 2", len(recs))
	}

	removeVMRecord(rec.Name)
	if r := readVMRecord(rec.Name); r.Name != "" {
		t.Errorf("record survived remove: %+v", r)
	}
	if len(listVMRecords()) != 1 {
		t.Errorf("listVMRecords after remove = %d, want 1", len(listVMRecords()))
	}
	// Idempotent remove.
	removeVMRecord(rec.Name)
}

// TestReadVMRecordMissing pins the zero-value fallbacks that make an unknown
// machine read as stale rather than as a fabricated match.
func TestReadVMRecordMissing(t *testing.T) {
	t.Setenv("SANDBOXER_STATE", t.TempDir())
	if r := readVMRecord("nope"); r.Name != "" || r.Hash != "" {
		t.Errorf("missing record = %+v, want zero", r)
	}
	// A non-JSON file reads as zero, not a crash.
	dir := vmMachinesDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "junk.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := readVMRecord("junk"); r.Name != "" {
		t.Errorf("junk record = %+v, want zero", r)
	}
	// listVMRecords skips the unparseable file.
	if recs := listVMRecords(); len(recs) != 0 {
		t.Errorf("listVMRecords with only junk = %d, want 0", len(recs))
	}
}

// TestWriteVMRecordNoStateRoot pins that a machine record cannot be silently
// dropped when there is nowhere to put it.
func TestWriteVMRecordNoStateRoot(t *testing.T) {
	t.Setenv("SANDBOXER_STATE", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "")
	if vmMachinesDir() != "" {
		t.Skip("a state root is still resolvable in this environment")
	}
	if err := writeVMRecord(vmRecord{Name: "x"}); err == nil {
		t.Error("writeVMRecord with no state root must error")
	}
}

// TestParseVMMachines pins the `machine ls --json` decode, including the
// malformed-output fallback.
func TestParseVMMachines(t *testing.T) {
	in := `[{"name":"a","state":"running","pid":1},{"name":"b","state":"stopped"}]`
	got := parseVMMachines([]byte(in))
	want := []vmMachine{{Name: "a", State: "running"}, {Name: "b", State: "stopped"}}
	if !slices.Equal(got, want) {
		t.Errorf("parseVMMachines = %+v, want %+v", got, want)
	}
	if parseVMMachines([]byte("garbage")) != nil {
		t.Error("malformed json must yield nil")
	}
}
