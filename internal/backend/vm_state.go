package backend

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/irasikhin/sandboxer/internal/config"
)

// The host-side machine records. microsandbox DOES stamp labels on a machine,
// but the identity a session needs (the slug and base it belongs to, the
// config hash it was created with, its image id and mount fingerprint) lives
// in a host-side record: one JSON file per machine at
// <state root>/machines/microsandbox/<name>.json. One identity mechanism means
// one set of sweeps and no engine-store drift; the labels only make a machine
// identifiable via `msb list` alone. The machine name is globally unique
// (SessionName folds an 8-hex of the base dir into it), so a per-name file
// needs no lock — every writer owns a distinct path — and a by-name lookup
// (InspectSession) and a base-dir sweep (SessionStates / OrphanSessions) both
// read straight from that directory.
//
// The "microsandbox" subdirectory is load-bearing: existing machines recorded
// there by earlier releases must still be found, so the path never changes
// with the code layout. (The retired smolvm runner used the flat machines/
// root; its records are stale data a `clean` never sees, not ours to migrate.)

// errNoStateRoot is returned when no state root can be resolved (no home, no
// override), so a machine record cannot be persisted.
var errNoStateRoot = errors.New("no state root (set HOME, XDG_STATE_HOME or SANDBOXER_STATE)")

// vmRecord is the host-side identity of one microVM session. ImageID/MountIDs
// are omitted when empty so a record stays diffable.
type vmRecord struct {
	Name     string `json:"name"`
	BaseDir  string `json:"baseDir"`
	Slug     string `json:"slug"`
	Hash     string `json:"hash"`
	ImageID  string `json:"imageID,omitempty"`
	MountIDs string `json:"mountIDs,omitempty"`
}

// vmMachinesDir is the directory of the per-machine records, or "" when no
// state root exists (callers then behave as if there were no records).
func vmMachinesDir() string {
	root := config.StateRoot()
	if root == "" {
		return ""
	}
	return filepath.Join(root, "machines", msbEngine)
}

func vmRecordPath(name string) string {
	dir := vmMachinesDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, name+".json")
}

// writeVMRecord persists rec atomically (write-temp + rename), creating the
// machines dir on demand.
func writeVMRecord(rec vmRecord) error {
	path := vmRecordPath(rec.Name)
	if path == "" {
		return errNoStateRoot
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readVMRecord loads a machine's record; the zero vmRecord (all fields "") when
// the file is absent or unreadable, which reads through vmInspectSession as an
// "unknown" hash — i.e. stale, recreated — never a fabricated match.
func readVMRecord(name string) vmRecord {
	path := vmRecordPath(name)
	if path == "" {
		return vmRecord{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return vmRecord{}
	}
	var rec vmRecord
	if json.Unmarshal(data, &rec) != nil {
		return vmRecord{}
	}
	return rec
}

// removeVMRecord deletes a machine's record (idempotent — a missing file is not
// an error).
func removeVMRecord(name string) {
	if path := vmRecordPath(name); path != "" {
		_ = os.Remove(path)
	}
}

// listVMRecords returns every machine record persisted for the engine; a
// read/parse failure on one file skips it rather than aborting the sweep.
// Subdirectories are skipped, so foreign files never leak into a sweep.
func listVMRecords() []vmRecord {
	dir := vmMachinesDir()
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var recs []vmRecord
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if rec := readVMRecord(strings.TrimSuffix(e.Name(), ".json")); rec.Name != "" {
			recs = append(recs, rec)
		}
	}
	return recs
}

// vmMachine is the subset of the engine inventory a session cares about: the
// name and the run state ("running"/"stopped").
type vmMachine struct {
	Name  string
	State string
}

// vmMachineByName finds a live machine by name in the engine's inventory.
func vmMachineByName(name string) (vmMachine, bool) {
	for _, m := range msbListMachines() {
		if m.Name == name {
			return m, true
		}
	}
	return vmMachine{}, false
}
