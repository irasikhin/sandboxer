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
	"strings"

	"github.com/irasikhin/sandboxer/internal/execx"
)

// The microVM session lifecycle: the smolvm twin of session.go's container
// path, over the SAME pure planSession policy so the two can never disagree on
// when a session is stale. smolvm has named machines but no labels, so identity
// lives in a host-side record (vm_state.go) rather than the engine; everything
// else — the decision table, the tmux capture/restore, the enter orchestration
// in internal/cli — is shared. Every engine call goes through smolvmBin(), the
// real binary behind the "smolvm" identity (see detect.go).

// vmSessionHashArgv is the canonical config argv a microVM session's hash is
// taken over: the create argv MINUS the machine name (a rename must never flip
// the hash), so any change to the image, mounts, env, size or the egress
// allowlist recreates the machine while a rename does not — the microVM
// analogue of ConfigHash.
func vmSessionHashArgv(o RunOpts) []string {
	return append([]string{"machine", "create", "-I", o.Image}, vmCommonArgs(o)...)
}

// vmSessionWantHash computes the hash a fresh microVM session for o must carry.
// Pure (no engine call — the whole config is in the argv), NUL-joined so
// adjacent argv elements cannot collide by concatenation.
func vmSessionWantHash(o RunOpts) string {
	sum := sha256.Sum256([]byte(strings.Join(vmSessionHashArgv(o), "\x00")))
	return hex.EncodeToString(sum[:])
}

// vmInspectSession reports a machine's existence and run state from
// `machine ls`, and its recorded hash/image/mounts from the host-side record
// (smolvm keeps no labels). A machine with no record reads as stale (empty
// hash), which recreates it — never a fabricated match.
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
// configuration-fresh state and returns its machine name — the microVM twin of
// EnsureSession, over the shared planSession policy. Image freshness is not
// checked yet (the image store lands with the in-VM build); passing "" for the
// wanted image id simply skips that half, never a false "stale".
func vmEnsureSession(o RunOpts) (string, error) {
	name := SessionName(o.Slug, o.BaseDir)

	// Serialize concurrent converges of THIS machine across processes, like the
	// container path: the loser re-inspects under the lock and execs the winner's
	// machine instead of racing a second create. Best-effort.
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
		wantImage = vmImageID(o.Image)
	}
	switch planSession(info, hash, wantImage) {
	case actExec:
		return name, nil
	case actStart:
		if err := execx.Run(smolvmBin(), vmStartArgv(name)...); err != nil {
			return "", fmt.Errorf("start machine %s: %w", name, err)
		}
		return name, nil
	case actRecreate:
		notice(o.Stderr, "recreating session: "+staleReason(info, hash))
		return vmRecreateSession(o, name, hash)
	default: // actCreate
		return vmCreateSession(o, name, hash)
	}
}

// vmCreateSession creates the machine, starts it (smolvm's create does not
// start — spike: create → start → exec), and records its identity. A create
// that loses a name race to a concurrent enter adopts the winner's running,
// hash-fresh machine instead of surfacing the conflict.
func vmCreateSession(o RunOpts, name, hash string) (string, error) {
	// Resolve the toolbox image to its store tar (building it in a microVM on
	// first use); a public ref passes through. imageID is recorded so a rebuilt
	// image under the same name reads as stale on the next enter.
	imageRef, err := vmEnsureImage(o)
	if err != nil {
		return "", err
	}
	imageID := vmImageID(o.Image)
	o.Image = imageRef

	// Stage the profile.json into the per-sandbox run dir so the -v mount source
	// exists before the machine boots.
	if err := stageProfileJSON(o); err != nil {
		return "", fmt.Errorf("stage profile.json for %s: %w", name, err)
	}

	notice(o.Stderr, "creating the session machine…")
	cmd := exec.Command(smolvmBin(), vmCreateArgv(o, name)...)
	cmd.Stdout = io.Discard
	cmd.Stderr = o.Stderr
	if err := cmd.Run(); err != nil {
		if again := vmInspectSession(name); again.Exists && again.Running && again.Hash == hash {
			return name, nil
		}
		return "", fmt.Errorf("create machine %s: %w", name, err)
	}
	if err := execx.Run(smolvmBin(), vmStartArgv(name)...); err != nil {
		return "", fmt.Errorf("start machine %s: %w", name, err)
	}
	if err := writeVMRecord(vmRecord{Name: name, BaseDir: o.BaseDir, Slug: o.Slug, Hash: hash, ImageID: imageID, MountIDs: o.MountIDs}); err != nil {
		notice(o.Stderr, "warning: could not record session state: "+err.Error())
	}
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
	if err := execx.Run(smolvmBin(), vmRemoveArgv(name)...); err != nil {
		return "", fmt.Errorf("remove stale machine %s: %w", name, err)
	}
	removeVMRecord(name)
	return vmCreateSession(o, name, hash)
}

// vmExecSession runs cmdArgs inside the running machine and returns the exit
// code — the microVM counterpart of ExecSession. The --secret-env references in
// vmExecArgv resolve against the child process environment, so the auth VALUES
// are placed there (never in argv), current on every exec.
func vmExecSession(o RunOpts, name string, cmdArgs []string) (int, error) {
	cmd := exec.Command(smolvmBin(), vmExecArgv(o, name, cmdArgs)...)
	cmd.Stdin = o.Stdin
	cmd.Stdout = o.Stdout
	cmd.Stderr = o.Stderr
	cmd.Env = append(os.Environ(), o.AuthEnv...)
	return exitCode(cmd.Run()), nil
}

// vmRun executes a one-shot ephemeral machine — the microVM counterpart of Run
// — and returns its exit code. The agent is the machine's workload, so its auth
// env is placed on the child process for the --secret-env references (see
// vmRunArgv) to resolve.
func vmRun(o RunOpts) int {
	imageRef, err := vmEnsureImage(o)
	if err != nil {
		if o.Stderr != nil {
			fmt.Fprintf(o.Stderr, "sandboxer: %v\n", err)
		}
		return 1
	}
	o.Image = imageRef
	if err := stageProfileJSON(o); err != nil {
		if o.Stderr != nil {
			fmt.Fprintf(o.Stderr, "sandboxer: %v\n", err)
		}
		return 1
	}
	cmd := exec.Command(smolvmBin(), vmRunArgv(o)...)
	cmd.Stdin = o.Stdin
	cmd.Stdout = o.Stdout
	cmd.Stderr = o.Stderr
	cmd.Env = append(os.Environ(), o.AuthEnv...)
	return exitCode(cmd.Run())
}

// vmStopSession stops slug's machine, keeping its config and volumes so a later
// ensure resumes it with a plain start. Idempotent: a missing machine is not an
// error.
func vmStopSession(slug, baseDir string) error {
	name := SessionName(slug, baseDir)
	if _, ok := vmMachineByName(name); ok {
		if err := execx.Run(smolvmBin(), vmStopArgv(name)...); err != nil {
			return fmt.Errorf("stop machine %s: %w", name, err)
		}
	}
	return nil
}

// vmRemoveSession deletes slug's machine and its record. Idempotent.
func vmRemoveSession(slug, baseDir string) error {
	return vmRemoveMachineByName(SessionName(slug, baseDir))
}

func vmRemoveMachineByName(name string) error {
	if _, ok := vmMachineByName(name); ok {
		if err := execx.Run(smolvmBin(), vmRemoveArgv(name)...); err != nil {
			return fmt.Errorf("remove machine %s: %w", name, err)
		}
	}
	removeVMRecord(name)
	return nil
}

// vmRemoveAllSessions deletes every recorded machine created from baseDir, each
// with its record. Failures are collected so one stubborn machine does not
// strand the rest.
func vmRemoveAllSessions(baseDir string) error {
	var errs []error
	for _, rec := range listVMRecords() {
		if rec.BaseDir == baseDir {
			errs = append(errs, vmRemoveMachineByName(rec.Name))
		}
	}
	return errors.Join(errs...)
}

// vmSessionStates maps each of baseDir's session slugs to its machine run state
// for listings: the recorded machines cross-referenced with the live inventory.
// A recorded machine the engine no longer knows reads as "gone", so a
// hand-deleted machine still surfaces (for cleanup) rather than vanishing.
func vmSessionStates(baseDir string) (map[string]string, error) {
	live := map[string]string{}
	for _, m := range vmListMachines() {
		live[m.Name] = m.State
	}
	states := map[string]string{}
	for _, rec := range listVMRecords() {
		if rec.BaseDir != baseDir {
			continue
		}
		if st, ok := live[rec.Name]; ok {
			states[rec.Slug] = st
		} else {
			states[rec.Slug] = "gone"
		}
	}
	return states, nil
}

// vmOrphanSessions returns the names of recorded machines whose base directory
// no longer exists on this host — the project was deleted behind sandboxer's
// back — for doctor to report with a removal hint.
func vmOrphanSessions() ([]string, error) {
	var orphans []string
	for _, rec := range listVMRecords() {
		if rec.BaseDir == "" {
			continue
		}
		if _, err := os.Stat(rec.BaseDir); os.IsNotExist(err) {
			orphans = append(orphans, rec.Name)
		}
	}
	return orphans, nil
}
