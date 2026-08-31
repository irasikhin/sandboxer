package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os/exec"
	"strings"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/style"
)

// Labels stamped on persistent sessions so they can be identified through the
// engine. The SOURCE OF TRUTH for a session's identity stays the host-side
// record (vm_state.go); the labels only make a machine identifiable via
// `msb list` alone.
const (
	// LabelManaged marks a machine as created by sandboxer (value "true").
	LabelManaged = "sandboxer.managed"
	// LabelSlug records the sandbox slug the session belongs to.
	LabelSlug = "sandboxer.slug"
	// LabelBase records the host state dir the session was created from.
	LabelBase = "sandboxer.base"
	// LabelHash records the session hash the machine was created with; a
	// mismatch against the freshly computed hash means the desired
	// configuration changed and the running session is stale.
	LabelHash = "sandboxer.hash"
	// LabelMounts records the individual source mounts' on-disk identities the
	// session was created with (RunOpts.MountIDs). LabelHash says THAT the
	// desired configuration moved; diffing this against a fresh resolve says
	// what — which view directory appeared, which one a host-side checkout
	// recreated, which one is gone. Empty on sessions created before it existed,
	// which callers must treat as "unknown", never as "nothing was mounted".
	LabelMounts = "sandboxer.mounts"
)

// sessionNamePrefix marks a machine as sandboxer's — the conservative
// ownership evidence a sweep over the live inventory goes on when a machine's
// record was lost (vmOrphanSessions).
const sessionNamePrefix = "sandboxer-"

// SessionName returns the deterministic machine name for slug's persistent
// session under baseDir: "sandboxer-<slug>-<hash>". The slug is sanitized to
// a filesystem/engine-safe alphabet, and the 8-hex sha256 prefix of baseDir
// disambiguates same-named sandboxes living in different projects.
func SessionName(slug, baseDir string) string {
	return sessionNamePrefix + sanitizeContainerName(slug) + "-" + shortHash(baseDir)
}

// sanitizeContainerName maps s onto the machine-name alphabet the engine
// accepts ([a-zA-Z0-9_.-]); every other rune becomes '-'. The stricter
// leading-character rule is satisfied by SessionName's prefix.
func sanitizeContainerName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '_', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// shortHash returns the first 8 hex characters of sha256(s).
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:4])
}

// guestExec builds the command that runs argv non-interactively inside the
// already-running guest of the session named name, its stdout captured by the
// caller. It is the ONE engine primitive the session machinery leans on — the
// tmux layout capture, the agent process listing and the idleness probe all
// reach into the guest the same way, so every reader of guest state shares one
// "exec in the guest" shape.
func guestExec(_, name string, argv ...string) *exec.Cmd {
	return exec.Command(msbBin(), msbGuestExecArgv(name, argv)...)
}

// --- session lifecycle -------------------------------------------------------

// SessionWantHash computes the session hash a fresh persistent session for o
// must carry. The single staleness oracle for EnsureSession and the CLI's exec
// routing — they must never disagree.
func SessionWantHash(o RunOpts) string {
	return vmSessionWantHash(o)
}

// InspectSession reads the session machine's running state, recorded config
// hash and image ID. A machine the engine does not know is the zero
// SessionInfo.
func InspectSession(_, name string) SessionInfo {
	return vmInspectSession(name)
}

// SessionPorts reports the forwards the named machine publishes RIGHT NOW, as
// the runner itself sees them — not what the profile asks for. The two differ
// exactly when it matters: a forward lives in the create argv, so a session
// created before a port was configured publishes nothing while the config reads
// perfectly right, and that gap is what turns into "unable to connect" in a
// browser. nil means unknown (no machine, no runner, unreadable output).
func SessionPorts(_, name string) []config.Port {
	return msbSessionPorts(name)
}

// SessionIdle reports whether the session machine holds NOTHING worth
// preserving: its in-guest tmux server (the one `enter` attaches, socket
// "sandboxer") runs no session at all. It is the deciding fact when a running
// session's configuration went stale — an idle one can be converged on the
// spot, a busy one holds the user's agent and must be attached instead.
//
// Idleness is a POSITIVE finding, never an assumption: only a clean listing
// that came back empty, or tmux itself reporting no server, counts. Every
// other outcome (engine error, no tmux in the image, an unreachable guest)
// answers "not idle", because the cost of being wrong is asymmetric — a false
// "busy" merely postpones a config change to the next stop/enter, while a
// false "idle" destroys a running agent.
func SessionIdle(engine, name string) bool {
	cmd := guestExec(engine, name, "tmux", "-L", "sandboxer", "list-sessions")
	var errb strings.Builder
	cmd.Stderr = &errb
	out, err := cmd.Output()
	if err == nil {
		return strings.TrimSpace(string(out)) == ""
	}
	// tmux exits non-zero when its server is not running at all — that is the
	// definitive "there is nothing in there" answer, not a failure.
	return strings.Contains(errb.String(), "no server running")
}

// EnsureSession converges slug's persistent session machine to a running,
// configuration-fresh state and returns its name, ready for ExecSession. It
// is the thin driver around the pure planSession policy: one inspect, one
// plan, one action. Re-running EnsureSession after any outcome is always safe.
func EnsureSession(o RunOpts) (string, error) {
	return vmEnsureSession(o)
}

// ExecSession runs cmdArgs inside the (already ensured) session machine and
// returns the command's exit code — the persistent-session counterpart of Run,
// with the same stdio wiring.
func ExecSession(o RunOpts, name string, cmdArgs []string) (int, error) {
	return vmExecSession(o, name, cmdArgs)
}

// StopSession stops slug's session machine, keeping its config and volumes in
// place so a later EnsureSession resumes it with a plain start. Idempotent: a
// missing or already-stopped session is not an error.
func StopSession(_, slug, baseDir string) error {
	return vmStopSession(slug, baseDir)
}

// RemoveSession removes slug's session machine and its host-side record
// entirely. Idempotent.
func RemoveSession(_, slug, baseDir string) error {
	return vmRemoveMachineByName(SessionName(slug, baseDir))
}

// RemoveSessionAnywhere removes slug's session from every engine on this host
// and reports the engines a session was actually found on.
//
// A teardown must look where the session IS, not where the profile says the
// NEXT one would be created: the session name is derived from (slug, state
// dir) alone, so a by-name removal can only ever hit this sandbox's own
// session. With one engine the sweep is a single probe, but the by-name
// contract (and the engines-found report the callers print) stays.
//
// Best-effort by contract: engine failures are collected and returned together
// so one unreachable engine cannot strand the file cleanup (the caller warns
// and still removes the files).
func RemoveSessionAnywhere(slug, baseDir string, d config.Defaults) ([]string, error) {
	name := SessionName(slug, baseDir)
	var removed []string
	var errs []error
	for _, engine := range SweepEngines(d) {
		had := sessionPresent(name)
		err := vmRemoveMachineByName(name)
		switch {
		case err != nil:
			errs = append(errs, err)
		case had:
			removed = append(removed, engine)
		}
	}
	return removed, errors.Join(errs...)
}

// SessionEngine reports which engine on this host actually holds the session
// named for (slug, baseDir), or "" when none does — what an operation that
// acts on ONE live session (stop, and its tmux capture) must target. Same
// reasoning as RemoveSessionAnywhere.
func SessionEngine(slug, baseDir string, d config.Defaults) string {
	name := SessionName(slug, baseDir)
	for _, engine := range SweepEngines(d) {
		if sessionPresent(name) {
			return engine
		}
	}
	return ""
}

// sessionPresent reports whether the engine still holds anything for the
// session named name: the host-side record counts as well as the machine — a
// record left behind by a hand-deleted machine is exactly the litter a
// teardown is there to reclaim.
func sessionPresent(name string) bool {
	if _, ok := vmMachineByName(name); ok {
		return true
	}
	return readVMRecord(name).Name != ""
}

// RemoveAllSessions removes every sandboxer-managed session created from
// baseDir, each with its record. Failures are collected so one stubborn
// session does not strand the rest.
func RemoveAllSessions(_, baseDir string) error {
	return vmRemoveAllSessions(baseDir)
}

// SessionStates maps each of baseDir's session slugs to its machine status
// ("running", "stopped", "gone", …) for listings.
func SessionStates(_, baseDir string) (map[string]string, error) {
	return vmSessionStates(baseDir)
}

// AllSessionStates maps baseDir → slug → machine status for EVERY
// sandboxer-managed session on the engine — what a host-wide listing needs.
func AllSessionStates(_ string) (map[string]map[string]string, error) {
	return vmAllSessionStates()
}

// OrphanSessions returns the names of sandboxer-managed session machines that
// nothing will ever match again — the project deleted behind sandboxer's back,
// or the host-side record lost. Reported by doctor with a removal hint;
// sandboxer itself never auto-removes them.
func OrphanSessions(_ string) ([]string, error) {
	return vmOrphanSessions()
}

// notice prints a one-line lifecycle notice to the user's stderr (nil-safe).
func notice(w io.Writer, msg string) {
	if w != nil {
		style.Infof(w, "%s", msg)
	}
}
