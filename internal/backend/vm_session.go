package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/irasikhin/sandboxer/internal/execx"
	"github.com/irasikhin/sandboxer/internal/style"
)

// The microVM session lifecycle over the pure planSession policy. The engine
// vocabulary lives in msb.go (the argv builders); identity lives in a
// host-side record (vm_state.go) rather than the engine, so the sweeps read
// one store. The decision table, the tmux capture/restore and the enter
// orchestration in internal/cli sit on top of this file.

// vmSessionWantHash computes the hash a fresh microVM session for o must
// carry: sha256 over the canonical config argv (msbHashArgv — the create argv
// minus the name and labels), NUL-joined so adjacent argv elements cannot
// collide by concatenation. Pure: no engine call, the whole config is in the
// argv. The microVM analogue of the container-era ConfigHash.
func vmSessionWantHash(o RunOpts) string {
	sum := sha256.Sum256([]byte(strings.Join(msbHashArgv(o), "\x00")))
	return hex.EncodeToString(sum[:])
}

// vmInspectSession reports a machine's existence and run state from the
// engine's inventory, and its recorded hash/image/mounts from the host-side
// record. A machine with no record reads as stale (empty hash), which
// recreates it — never a fabricated match.
func vmInspectSession(name string) SessionInfo {
	m, ok := vmMachineByName(name)
	if !ok {
		return SessionInfo{}
	}
	rec := readVMRecord(name)
	return SessionInfo{
		Exists:  true,
		Running: m.State == "running",
		Hash:    rec.Hash,
		ImageID: rec.ImageID,
		Mounts:  rec.MountIDs,
	}
}

// vmEnsureSession converges slug's microVM session to a running,
// configuration-fresh state and returns its machine name — the driver behind
// EnsureSession, over the shared planSession policy.
func vmEnsureSession(o RunOpts) (string, error) {
	name := SessionName(o.Slug, o.BaseDir)

	// Serialize concurrent converges of THIS machine across processes: the
	// loser re-inspects under the lock and execs the winner's machine instead
	// of racing a second create. Best-effort.
	if o.BaseDir != "" {
		if err := os.MkdirAll(o.BaseDir, 0o700); err == nil {
			if release, lerr := lockFile(filepath.Join(o.BaseDir, "."+name+".lock")); lerr == nil {
				defer release()
			}
		}
	}

	hash := vmSessionWantHash(o)
	info := vmInspectSession(name)
	// The wanted image id only matters for an existing machine, and is read from
	// whatever is in the store now: a not-yet-built image yields "" and the
	// freshness check is skipped, never a false "stale".
	wantImage := ""
	if info.Exists {
		wantImage = msbImageID(o.Image)
	}
	switch planSession(info, hash, wantImage) {
	case actExec:
		return name, nil
	case actStart:
		if err := execx.Run(msbBin(), msbStartArgv(name)...); err != nil {
			return "", fmt.Errorf("start machine %s: %w", name, err)
		}
		startPodmanService(name)
		return name, nil
	case actRecreate:
		notice(o.Stderr, "recreating session: "+staleReason(info, hash))
		return vmRecreateSession(o, name, hash)
	default: // actCreate
		return vmCreateSession(o, name, hash)
	}
}

// startPodmanService brings the guest's docker-compatible API socket up as
// part of BOOTING a machine, so it is there for every workload — not only the
// interactive shells whose rc.sh starts it lazily. An agent driven through
// `exec` (the common headless case) never sources that rc, and Testcontainers
// or a docker SDK then found DOCKER_HOST pointing at nothing.
//
// Called only where a machine just booted (create/start): the service has no
// timeout, so it lives as long as the machine, and paying an extra exec on
// every enter/exec would tax the hot path for nothing. Best-effort by design —
// a sandbox whose workload never touches the engine API must not fail to start
// because this did; the in-image script is idempotent (an older cached image
// without it simply is not there, and the exec fails harmlessly).
func startPodmanService(name string) {
	_ = execx.Run(msbBin(), msbGuestExecArgv(name, []string{podmanSocketBin})...)
}

// vmCreateSession creates the machine and records its identity. `msb create`
// already BOOTS the sandbox, so no separate start follows. A create that loses
// a name race to a concurrent enter adopts the winner's running, hash-fresh
// machine instead of surfacing the conflict.
func vmCreateSession(o RunOpts, name, hash string) (string, error) {
	if err := msbPreflight(o); err != nil {
		return "", err
	}
	// Resolve the toolbox image: a variant is built into msb's store on first
	// use; a public ref (the prebuilt default included) passes through for
	// msb to pull at create time.
	image := o.Image
	imageRef, err := msbEnsureImage(o)
	if err != nil {
		return "", err
	}
	o.Image = imageRef

	// Stage the profile.json into the per-sandbox run dir so the -v mount source
	// exists before the machine boots.
	if err := stageProfileJSON(o); err != nil {
		return "", fmt.Errorf("stage profile.json for %s: %w", name, err)
	}

	notice(o.Stderr, "creating the session machine…")
	cmd := exec.Command(msbBin(), msbCreateArgv(o, name, hash)...)
	cmd.Stdout = io.Discard
	cmd.Stderr = o.Stderr
	// The auth values ride on the CHILD process env, never in argv: in
	// microsandbox's --secret mode the create argv holds only KEY references and
	// msb reads the values from here.
	cmd.Env = append(os.Environ(), o.AuthEnv...)
	if err := cmd.Run(); err != nil {
		if again := vmInspectSession(name); again.Exists && again.Running && again.Hash == hash {
			return name, nil
		}
		return "", fmt.Errorf("create machine %s: %w%s", name, err, msbCreateFailHint(image))
	}
	// The image id is read AFTER the create so a create-time pull records the
	// digest it just fetched (before it, the ref was not in msb's store and
	// the id read as "unknown" — a later `image pull` then never read as
	// stale). A rebuilt or re-pulled image under the same name now recreates
	// the session on the next enter.
	imageID := msbImageID(image)
	if err := writeVMRecord(vmRecord{Name: name, BaseDir: o.BaseDir, Slug: o.Slug, Hash: hash, ImageID: imageID, MountIDs: o.MountIDs}); err != nil {
		notice(o.Stderr, "warning: could not record session state: "+err.Error())
	}
	startPodmanService(name)
	return name, nil
}

// vmRecreateSession replaces a stale machine: save the tmux layout while it
// still runs (so the next attach restores it — a config change no longer costs
// the user their windows), delete the old machine and its record, then create
// anew.
func vmRecreateSession(o RunOpts, name, hash string) (string, error) {
	if SaveSessionState(o.Engine, name, o.SessionStatePath) {
		notice(o.Stderr, "saved your session layout — restoring it on attach "+
			"(recorded agents relaunch and resume; other running programs are interrupted)")
	}
	notice(o.Stderr, "removing the old session machine…")
	if err := execx.Run(msbBin(), msbRemoveArgv(name)...); err != nil {
		return "", fmt.Errorf("remove stale machine %s: %w", name, err)
	}
	removeVMRecord(name)
	return vmCreateSession(o, name, hash)
}

// vmExecSession runs cmdArgs inside the running machine and returns the exit
// code — the driver behind ExecSession. The auth VALUES travel with the exec
// (as --env flags, or in --secret mode as boot-time references), so a rotated
// token reaches the sandbox on the next shell with no rebuild. profile.json is
// re-staged first: the share is a SNAPSHOT taken at create/run time, so an
// exec that reads it would otherwise see the stale copy; re-staging puts the
// current profile in front of the guest with no recreate (the file contents
// are not part of the session hash).
func vmExecSession(o RunOpts, name string, cmdArgs []string) (int, error) {
	if err := stageProfileJSON(o); err != nil {
		return 1, fmt.Errorf("stage profile.json for %s: %w", name, err)
	}
	cmd := exec.Command(msbBin(), msbExecArgv(o, name, cmdArgs)...)
	cmd.Stdin = o.Stdin
	cmd.Stdout = o.Stdout
	cmd.Stderr = o.Stderr
	cmd.Env = append(os.Environ(), o.AuthEnv...)
	return exitCode(cmd.Run()), nil
}

// vmRun executes a one-shot ephemeral machine — the microVM counterpart of Run
// — and returns its exit code. The agent is the machine's workload, so its auth
// env travels with the run itself.
func vmRun(o RunOpts) int {
	if err := msbPreflight(o); err != nil {
		if o.Stderr != nil {
			style.Errorf(o.Stderr, "%v", err)
		}
		return 1
	}
	imageRef, err := msbEnsureImage(o)
	if err != nil {
		if o.Stderr != nil {
			style.Errorf(o.Stderr, "%v", err)
		}
		return 1
	}
	o.Image = imageRef
	if err := stageProfileJSON(o); err != nil {
		if o.Stderr != nil {
			style.Errorf(o.Stderr, "%v", err)
		}
		return 1
	}
	cmd := exec.Command(msbBin(), msbRunArgv(o)...)
	cmd.Stdin = o.Stdin
	cmd.Stdout = o.Stdout
	cmd.Stderr = o.Stderr
	cmd.Env = append(os.Environ(), o.AuthEnv...)
	return exitCode(cmd.Run())
}

// vmRunDir is the per-sandbox directory shared read-only at /run/sandboxer, or
// "" when no profile.json is configured. msb cannot share the single
// profile.json file (it shares directories), and _meta holds every sandbox's
// metadata, so the file is staged into its own <slug>.run dir beside it.
func vmRunDir(o RunOpts) string {
	if o.ProfileJSONPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(o.ProfileJSONPath), o.Slug+".run")
}

// stageProfileJSON populates the per-sandbox run dir (vmRunDir) with the
// profile.json so it can be shared at /run/sandboxer. The dir is always created
// when a profile.json path is configured (so the -v source exists even if the
// file itself is absent); the file is copied when present. No-op otherwise.
func stageProfileJSON(o RunOpts) error {
	dir := vmRunDir(o)
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if !pathExists(o.ProfileJSONPath) {
		return nil
	}
	data, err := os.ReadFile(o.ProfileJSONPath)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "profile.json"), data, 0o600)
}

// vmStopSession stops slug's machine, keeping its config and volumes so a later
// ensure resumes it with a plain start. Idempotent: a missing machine is not an
// error.
func vmStopSession(slug, baseDir string) error {
	name := SessionName(slug, baseDir)
	if _, ok := vmMachineByName(name); ok {
		if err := execx.Run(msbBin(), msbStopArgv(name)...); err != nil {
			return fmt.Errorf("stop machine %s: %w", name, err)
		}
	}
	return nil
}

// vmRemoveMachineByName deletes the named machine and its record. Idempotent.
func vmRemoveMachineByName(name string) error {
	if _, ok := vmMachineByName(name); ok {
		if err := execx.Run(msbBin(), msbRemoveArgv(name)...); err != nil {
			return fmt.Errorf("remove machine %s: %w", name, err)
		}
	}
	removeVMRecord(name)
	return nil
}

// vmRemoveAllSessions deletes every machine recorded from baseDir, each with
// its record, plus any RECORDLESS live machine whose SessionName still binds
// it to baseDir — a machine whose record was lost (a wiped state dir, a
// changed SANDBOXER_STATE) would otherwise be invisible to clean and survive
// it. The name suffix is this project's 8-hex base hash, so only this
// project's machines are matched, never another project's recordless
// leftovers. Failures are collected so one stubborn machine does not strand
// the rest.
func vmRemoveAllSessions(baseDir string) error {
	var errs []error
	removed := map[string]bool{}
	for _, rec := range listVMRecords() {
		if rec.BaseDir == baseDir {
			removed[rec.Name] = true
			errs = append(errs, vmRemoveMachineByName(rec.Name))
		}
	}
	for _, m := range msbListMachines() {
		if removed[m.Name] || !strings.HasPrefix(m.Name, sessionNamePrefix) {
			continue
		}
		if strings.HasSuffix(m.Name, "-"+shortHash(baseDir)) {
			errs = append(errs, vmRemoveMachineByName(m.Name))
		}
	}
	return errors.Join(errs...)
}

// vmSessionStates maps each of baseDir's session slugs to its machine run state
// for listings: the recorded machines cross-referenced with the live inventory.
// A recorded machine the engine no longer knows reads as "gone", so a
// hand-deleted machine still surfaces (for cleanup) rather than vanishing.
func vmSessionStates(baseDir string) (map[string]string, error) {
	all, err := vmAllSessionStates()
	if err != nil {
		return nil, err
	}
	states := all[baseDir]
	if states == nil {
		states = map[string]string{}
	}
	return states, nil
}

// vmAllSessionStates is AllSessionStates' engine: every recorded machine
// grouped by the base dir its record names, plus live machines whose record
// was lost surfaced under the synthetic "(unrecorded)" bucket so a host-wide
// listing is never blind to them (their base/slug lived in the record, so they
// cannot be attributed; doctor reports them for removal). There is no
// per-project query to save here — the records are host-side files that
// listVMRecords reads whole either way — so the per-project view is a lookup
// into this one.
func vmAllSessionStates() (map[string]map[string]string, error) {
	live := map[string]string{}
	recorded := map[string]bool{}
	for _, m := range msbListMachines() {
		live[m.Name] = m.State
	}
	states := map[string]map[string]string{}
	for _, rec := range listVMRecords() {
		recorded[rec.Name] = true
		if rec.BaseDir == "" {
			continue // no base dir — attributable to no project
		}
		st, ok := live[rec.Name]
		if !ok {
			st = "gone"
		}
		if states[rec.BaseDir] == nil {
			states[rec.BaseDir] = map[string]string{}
		}
		states[rec.BaseDir][rec.Slug] = st
	}
	for _, m := range msbListMachines() {
		if recorded[m.Name] || !strings.HasPrefix(m.Name, sessionNamePrefix) {
			continue
		}
		if states["(unrecorded)"] == nil {
			states["(unrecorded)"] = map[string]string{}
		}
		states["(unrecorded)"][m.Name] = m.State
	}
	return states, nil
}

// vmOrphanSessions returns the names of sandboxer-managed machines nothing will
// ever match again, for doctor to report with a removal hint. Two ways a machine
// gets there, and the second is why this consults the LIVE inventory and not
// only the records:
//
//   - recorded, but the base directory the record names is gone — the project
//     was deleted behind sandboxer's back (rm -rf instead of `sandboxer rm`);
//   - running, named like ours, but with NO record at all — the record was lost
//     instead (a wiped state dir, a changed SANDBOXER_STATE/XDG_STATE_HOME).
//
// Every other VM sweep iterates records, so before this the second kind was
// invisible to clean, list and doctor while still holding disk and a VM. The
// name prefix is deliberately conservative evidence: an unrecorded machine is
// reported for the user to remove, never auto-deleted.
func vmOrphanSessions() ([]string, error) {
	recorded := make(map[string]bool)
	var orphans []string
	for _, rec := range listVMRecords() {
		recorded[rec.Name] = true
		if rec.BaseDir == "" {
			continue // no base dir — not provably orphaned
		}
		if _, err := os.Stat(rec.BaseDir); os.IsNotExist(err) {
			orphans = append(orphans, rec.Name)
		}
	}
	for _, m := range msbListMachines() {
		if recorded[m.Name] || !strings.HasPrefix(m.Name, sessionNamePrefix) {
			continue
		}
		orphans = append(orphans, m.Name)
	}
	sort.Strings(orphans)
	return orphans, nil
}

// RemoveCommand renders a copy-pasteable command that removes the named
// sessions, for a hint like doctor's orphan line. msb removes one machine per
// call, so the hint renders one command per machine.
func RemoveCommand(_ string, names []string) string {
	if len(names) == 0 {
		return ""
	}
	cmds := make([]string, 0, len(names))
	for _, n := range names {
		cmds = append(cmds, msbBin()+" "+strings.Join(msbRemoveArgv(n), " "))
	}
	return strings.Join(cmds, "; ")
}
