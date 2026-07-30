package backend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/irasikhin/sandboxer/internal/execx"
)

// The microVM session lifecycle: the twin of session.go's container path, over
// the SAME pure planSession policy so the two can never disagree on when a
// session is stale. It is runner-agnostic — smolvm and microsandbox differ only
// in their CLI vocabulary, which lives behind vmRunner (vm_runner.go) — and
// identity lives in a host-side record (vm_state.go) rather than the engine, so
// both runners share one set of sweeps. Everything else (the decision table, the
// tmux capture/restore, the enter orchestration in internal/cli) is shared with
// the container backend.

// vmSessionHashArgv is the canonical config argv a smolvm session's hash is
// taken over: the create argv MINUS the machine name (a rename must never flip
// the hash), so any change to the image, mounts, env, size or the egress
// allowlist recreates the machine while a rename does not — the microVM
// analogue of ConfigHash. (microsandbox's equivalent is msbHashArgv, which also
// drops the labels.)
func vmSessionHashArgv(o RunOpts) []string {
	return append([]string{"machine", "create", "-I", o.Image}, vmCommonArgs(o)...)
}

// vmSessionWantHash computes the hash a fresh microVM session for o must carry.
// Pure (no engine call — the whole config is in the argv), NUL-joined so
// adjacent argv elements cannot collide by concatenation.
func vmSessionWantHash(o RunOpts) string {
	sum := sha256.Sum256([]byte(strings.Join(vmRunnerFor(o.Engine).hashArgv(o), "\x00")))
	return hex.EncodeToString(sum[:])
}

// vmInspectSession reports a machine's existence and run state from the
// runner's inventory, and its recorded hash/image/mounts from the host-side
// record. A machine with no record reads as stale (empty hash), which recreates
// it — never a fabricated match.
func vmInspectSession(r vmRunner, name string) SessionInfo {
	m, ok := vmMachineByName(r, name)
	if !ok {
		return SessionInfo{}
	}
	rec := readVMRecord(r, name)
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
// EnsureSession, over the shared planSession policy.
func vmEnsureSession(o RunOpts) (string, error) {
	r := vmRunnerFor(o.Engine)
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
	info := vmInspectSession(r, name)
	// The wanted image id only matters for an existing machine, and is read from
	// whatever is in the store now: a not-yet-built image yields "" and the
	// freshness check is skipped, never a false "stale".
	wantImage := ""
	if info.Exists {
		wantImage = r.imageID(o.Image)
	}
	switch planSession(info, hash, wantImage) {
	case actExec:
		return name, nil
	case actStart:
		if err := execx.Run(r.bin(), r.startArgv(name)...); err != nil {
			return "", fmt.Errorf("start machine %s: %w", name, err)
		}
		return name, nil
	case actRecreate:
		notice(o.Stderr, "recreating session: "+staleReason(info, hash))
		return vmRecreateSession(o, r, name, hash)
	default: // actCreate
		return vmCreateSession(o, r, name, hash)
	}
}

// vmCreateSession creates the machine, starts it when the runner's create does
// not (smolvm: create → start → exec; microsandbox boots on create), and records
// its identity. A create that loses a name race to a concurrent enter adopts the
// winner's running, hash-fresh machine instead of surfacing the conflict.
func vmCreateSession(o RunOpts, r vmRunner, name, hash string) (string, error) {
	if err := r.preflight(o); err != nil {
		return "", err
	}
	// Resolve the toolbox image to what the runner is handed (smolvm: the store
	// tar; microsandbox: a reference in its own image store), building it on
	// first use; a public ref passes through. imageID is recorded so a rebuilt
	// image under the same name reads as stale on the next enter.
	imageRef, err := r.ensureImage(o)
	if err != nil {
		return "", err
	}
	imageID := r.imageID(o.Image)
	o.Image = imageRef

	o.RT.Domains = vmCreatableDomains(o, r)

	// Stage the profile.json into the per-sandbox run dir so the -v mount source
	// exists before the machine boots.
	if err := stageProfileJSON(o); err != nil {
		return "", fmt.Errorf("stage profile.json for %s: %w", name, err)
	}

	notice(o.Stderr, "creating the session machine…")
	cmd := exec.Command(r.bin(), r.createArgv(o, name, hash)...)
	cmd.Stdout = io.Discard
	cmd.Stderr = o.Stderr
	// The auth values ride on the CHILD process env, never in argv: in
	// microsandbox's --secret mode the create argv holds only KEY references and
	// msb reads the values from here. smolvm's create references none and simply
	// ignores them.
	cmd.Env = append(os.Environ(), o.AuthEnv...)
	if err := cmd.Run(); err != nil {
		if again := vmInspectSession(r, name); again.Exists && again.Running && again.Hash == hash {
			return name, nil
		}
		return "", fmt.Errorf("create machine %s: %w", name, err)
	}
	if !r.startsOnCreate() {
		if err := execx.Run(r.bin(), r.startArgv(name)...); err != nil {
			return "", fmt.Errorf("start machine %s: %w", name, err)
		}
	}
	if err := writeVMRecord(r, vmRecord{Name: name, BaseDir: o.BaseDir, Slug: o.Slug, Hash: hash, ImageID: imageID, MountIDs: o.MountIDs}); err != nil {
		notice(o.Stderr, "warning: could not record session state: "+err.Error())
	}
	return name, nil
}

// vmCreatableDomains adapts the allowlist to what the runner can actually boot
// with. smolvm's --allow-host resolves every host at VM start and HARD-FAILS the
// machine on any that does not (e.g. wildcard-suffix domains like cloudfront.net
// have no A record of their own), so the unresolvable ones are dropped with a
// warning; microsandbox's rules are name-bound and matched at connect time, so
// nothing is dropped there. Only the launch argv is filtered — the session hash
// is taken over the full configured list, so a transient resolution failure
// never flips it. The proxy path uses no per-host flag and is left alone.
func vmCreatableDomains(o RunOpts, r vmRunner) []string {
	if _, isSmolvm := r.(smolvmRunner); !isSmolvm {
		return o.RT.Domains
	}
	if !egressRequired(o) || o.RT.Proxy != "" || len(o.RT.Domains) == 0 {
		return o.RT.Domains
	}
	return vmResolvableDomains(o.RT.Domains, o.Stderr)
}

// vmResolvableDomains keeps only the allowlist domains that resolve on the host,
// dropping the rest with a one-line warning — smolvm's --allow-host hard-fails
// the machine on an unresolvable host. Lookups run concurrently with a short
// timeout so a non-resolving host cannot stall create; input order is preserved.
func vmResolvableDomains(domains []string, w io.Writer) []string {
	type result struct {
		d  string
		ok bool
	}
	ch := make(chan result, len(domains))
	for _, d := range domains {
		go func(d string) {
			host := strings.TrimPrefix(strings.TrimSpace(d), ".")
			if host == "" {
				ch <- result{d, false}
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, err := net.DefaultResolver.LookupHost(ctx, host)
			ch <- result{d, err == nil}
		}(d)
	}
	keep := make(map[string]bool, len(domains))
	var dropped []string
	for range domains {
		if r := <-ch; r.ok {
			keep[r.d] = true
		} else {
			dropped = append(dropped, strings.TrimPrefix(r.d, "."))
		}
	}
	var out []string
	for _, d := range domains {
		if keep[d] {
			out = append(out, d)
		}
	}
	if len(dropped) > 0 && w != nil {
		sort.Strings(dropped)
		fmt.Fprintf(w, "sandboxer: egress: dropped %d unresolvable domain(s) from the microvm allowlist "+
			"(smolvm --allow-host needs resolvable hosts; wildcard-suffix domains need egress.proxy or "+
			"the microsandbox backend, whose rules are name-bound): %s\n",
			len(dropped), strings.Join(dropped, " "))
	}
	return out
}

// vmRecreateSession replaces a stale machine: save the tmux layout while it
// still runs (so the next attach restores it — a config change no longer costs
// the user their windows), delete the old machine and its record, then create
// anew.
func vmRecreateSession(o RunOpts, r vmRunner, name, hash string) (string, error) {
	if SaveSessionState(o.Engine, name, o.SessionStatePath) {
		notice(o.Stderr, "saved your session layout — restoring it on attach "+
			"(recorded agents relaunch and resume; other running programs are interrupted)")
	}
	notice(o.Stderr, "removing the old session machine…")
	if err := execx.Run(r.bin(), r.removeArgv(name)...); err != nil {
		return "", fmt.Errorf("remove stale machine %s: %w", name, err)
	}
	removeVMRecord(r, name)
	return vmCreateSession(o, r, name, hash)
}

// vmExecSession runs cmdArgs inside the running machine and returns the exit
// code — the microVM counterpart of ExecSession. The auth VALUES travel with
// the exec (as smolvm --secret-env references resolved from this process's
// environment, or as microsandbox --env flags), so a rotated token reaches the
// sandbox on the next shell with no rebuild.
func vmExecSession(o RunOpts, name string, cmdArgs []string) (int, error) {
	r := vmRunnerFor(o.Engine)
	cmd := exec.Command(r.bin(), r.execArgv(o, name, cmdArgs)...)
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
	r := vmRunnerFor(o.Engine)
	if err := r.preflight(o); err != nil {
		if o.Stderr != nil {
			fmt.Fprintf(o.Stderr, "sandboxer: %v\n", err)
		}
		return 1
	}
	imageRef, err := r.ensureImage(o)
	if err != nil {
		if o.Stderr != nil {
			fmt.Fprintf(o.Stderr, "sandboxer: %v\n", err)
		}
		return 1
	}
	o.Image = imageRef
	o.RT.Domains = vmCreatableDomains(o, r)
	if err := stageProfileJSON(o); err != nil {
		if o.Stderr != nil {
			fmt.Fprintf(o.Stderr, "sandboxer: %v\n", err)
		}
		return 1
	}
	cmd := exec.Command(r.bin(), r.runArgv(o)...)
	cmd.Stdin = o.Stdin
	cmd.Stdout = o.Stdout
	cmd.Stderr = o.Stderr
	cmd.Env = append(os.Environ(), o.AuthEnv...)
	return exitCode(cmd.Run())
}

// vmStopSession stops slug's machine, keeping its config and volumes so a later
// ensure resumes it with a plain start. Idempotent: a missing machine is not an
// error.
func vmStopSession(engine, slug, baseDir string) error {
	r := vmRunnerFor(engine)
	name := SessionName(slug, baseDir)
	if _, ok := vmMachineByName(r, name); ok {
		if err := execx.Run(r.bin(), r.stopArgv(name)...); err != nil {
			return fmt.Errorf("stop machine %s: %w", name, err)
		}
	}
	return nil
}

// vmRemoveSession deletes slug's machine and its record. Idempotent.
func vmRemoveSession(engine, slug, baseDir string) error {
	return vmRemoveMachineByName(vmRunnerFor(engine), SessionName(slug, baseDir))
}

func vmRemoveMachineByName(r vmRunner, name string) error {
	if _, ok := vmMachineByName(r, name); ok {
		if err := execx.Run(r.bin(), r.removeArgv(name)...); err != nil {
			return fmt.Errorf("remove machine %s: %w", name, err)
		}
	}
	removeVMRecord(r, name)
	return nil
}

// vmRemoveAllSessions deletes every machine this runner recorded from baseDir,
// each with its record. Failures are collected so one stubborn machine does not
// strand the rest.
func vmRemoveAllSessions(engine, baseDir string) error {
	r := vmRunnerFor(engine)
	var errs []error
	for _, rec := range listVMRecords(r) {
		if rec.BaseDir == baseDir {
			errs = append(errs, vmRemoveMachineByName(r, rec.Name))
		}
	}
	return errors.Join(errs...)
}

// vmSessionStates maps each of baseDir's session slugs to its machine run state
// for listings: the recorded machines cross-referenced with the live inventory.
// A recorded machine the engine no longer knows reads as "gone", so a
// hand-deleted machine still surfaces (for cleanup) rather than vanishing.
func vmSessionStates(engine, baseDir string) (map[string]string, error) {
	all, err := vmAllSessionStates(engine)
	if err != nil {
		return nil, err
	}
	states := all[baseDir]
	if states == nil {
		states = map[string]string{}
	}
	return states, nil
}

// vmAllSessionStates is AllSessionStates for a microVM runner: every recorded
// machine grouped by the base dir its record names. There is no per-project
// query to save here — the records are host-side files that listVMRecords reads
// whole either way — so the per-project view is a lookup into this one.
func vmAllSessionStates(engine string) (map[string]map[string]string, error) {
	r := vmRunnerFor(engine)
	live := map[string]string{}
	for _, m := range r.listMachines() {
		live[m.Name] = m.State
	}
	states := map[string]map[string]string{}
	for _, rec := range listVMRecords(r) {
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
	return states, nil
}

// vmOrphanSessions returns the names of recorded machines whose base directory
// no longer exists on this host — the project was deleted behind sandboxer's
// back — for doctor to report with a removal hint.
func vmOrphanSessions(engine string) ([]string, error) {
	var orphans []string
	for _, rec := range listVMRecords(vmRunnerFor(engine)) {
		if rec.BaseDir == "" {
			continue
		}
		if _, err := os.Stat(rec.BaseDir); os.IsNotExist(err) {
			orphans = append(orphans, rec.Name)
		}
	}
	return orphans, nil
}
