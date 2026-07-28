package backend

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/irasikhin/sandboxer/internal/config"
)

// A smolvm machine carries no engine-side labels — `machine ls` reports only
// name and state — so the identity a container keeps in its sandboxer.* labels
// (the slug and base it belongs to, the config hash it was created with, its
// image id and mount fingerprint) lives in a host-side record instead: one JSON
// file per machine at <state root>/machines/<name>.json. The machine name is
// globally unique (SessionName folds an 8-hex of the base dir into it), so a
// per-name file needs no lock — every writer owns a distinct path — and a
// by-name lookup (InspectSession) and a base-dir sweep (SessionStates /
// OrphanSessions) both read straight from that directory.
//
// microsandbox DOES have labels, but keeps the same record: one identity
// mechanism for both runners means one set of sweeps and no two-store drift. Its
// records live in a per-engine subdirectory (vmRunner.recordDir) because the
// machine name is derived from the sandbox, not the engine — a project that
// switches runners would otherwise have the new machine's record overwrite the
// old one's, and the old machine would be stranded with nothing left to name it.
// smolvm keeps the flat legacy layout, so existing records are found unchanged.

// errNoStateRoot is returned when no state root can be resolved (no home, no
// override), so a machine record cannot be persisted.
var errNoStateRoot = errors.New("no state root (set HOME, XDG_STATE_HOME or SANDBOXER_STATE)")

// vmRecord is the host-side identity of one microVM session — the microVM
// counterpart of a container's sandbox.* labels. ImageID/MountIDs are omitted
// when empty so a record stays diffable.
type vmRecord struct {
	Name     string `json:"name"`
	BaseDir  string `json:"baseDir"`
	Slug     string `json:"slug"`
	Hash     string `json:"hash"`
	ImageID  string `json:"imageID,omitempty"`
	MountIDs string `json:"mountIDs,omitempty"`
}

// vmMachinesDir is the directory of r's per-machine records, or "" when no state
// root exists (callers then behave as if there were no records).
func vmMachinesDir(r vmRunner) string {
	root := config.StateRoot()
	if root == "" {
		return ""
	}
	return filepath.Join(root, "machines", r.recordDir())
}

func vmRecordPath(r vmRunner, name string) string {
	dir := vmMachinesDir(r)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, name+".json")
}

// writeVMRecord persists rec atomically (write-temp + rename), creating the
// machines dir on demand.
func writeVMRecord(r vmRunner, rec vmRecord) error {
	path := vmRecordPath(r, rec.Name)
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
func readVMRecord(r vmRunner, name string) vmRecord {
	path := vmRecordPath(r, name)
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
func removeVMRecord(r vmRunner, name string) {
	if path := vmRecordPath(r, name); path != "" {
		_ = os.Remove(path)
	}
}

// listVMRecords returns every machine record persisted for r; a read/parse
// failure on one file skips it rather than aborting the sweep. Subdirectories
// are skipped, so another runner's records never leak into this one's sweep.
func listVMRecords(r vmRunner) []vmRecord {
	dir := vmMachinesDir(r)
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
		if rec := readVMRecord(r, strings.TrimSuffix(e.Name(), ".json")); rec.Name != "" {
			recs = append(recs, rec)
		}
	}
	return recs
}

// vmMachine is the subset of `machine ls --json` a session cares about: the
// name and the run state ("running"/"stopped").
type vmMachine struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

// parseVMMachines decodes `machine ls --json` output (pure, so it is unit-tested
// without a hypervisor); malformed output yields no machines rather than an
// error, mirroring the engine-query stance elsewhere.
func parseVMMachines(out []byte) []vmMachine {
	var machines []vmMachine
	if json.Unmarshal(out, &machines) != nil {
		return nil
	}
	return machines
}

// vmListMachines runs `machine ls --json` and returns the live machines. A
// package var so a test can inject a fake inventory; a nil/error result is
// "no machines", never a crash.
var vmListMachines = func() []vmMachine {
	out, err := exec.Command(smolvmBin(), vmListArgv()...).Output()
	if err != nil {
		return nil
	}
	return parseVMMachines(out)
}

// vmMachineByName finds a live machine by name in r's inventory.
func vmMachineByName(r vmRunner, name string) (vmMachine, bool) {
	for _, m := range r.listMachines() {
		if m.Name == name {
			return m, true
		}
	}
	return vmMachine{}, false
}
