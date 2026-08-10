package backend

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/toolbox"
)

// RunOpts configures a single machine invocation.
type RunOpts struct {
	Engine string
	Image  string
	Spec   toolbox.Spec // image variant customization; drives the auto-build of a missing variant
	Dest   string       // sandbox root (<slug>/), the workdir; shared rw only when MountDest
	// MountDest shares Dest itself rw — the whole sandbox root as one
	// stable window (a srcs edit shows up in a running session without
	// recreating it). It is set for a sandbox no source narrows.
	//
	// A narrowed sandbox clears it, and that is the CONTAINMENT BOUNDARY: the
	// worktrees under Dest are complete on the host, so sharing Dest would
	// hand the sandbox every excluded file. Unshared, they are unreachable —
	// what is not in SrcMounts does not exist inside. Never set this because a
	// mount seems to be missing; the false is what makes narrowing real. See
	// sandbox.Mounts, which decides both fields together.
	MountDest bool
	// SrcMounts are the source directories shared rw at their own host
	// paths: the adopted worktrees when MountDest (they live outside Dest), else
	// every source's exposed directories. Sorted by the caller: the order is
	// part of the session-hash contract.
	SrcMounts []string
	Slug      string
	BaseDir   string // host state dir (config.StateDir); names the persistent session (zero value fine for one-shot runs)
	// SessionStatePath is the host file where the session's tmux layout is saved
	// (backend.SaveSessionState) before this machine is replaced, so the next
	// attach can restore it. Empty disables capture (one-shot runs, exec). NEVER
	// part of the hashed argv — it changes nothing the machine runs with,
	// so setting it can never flip a session's hash.
	SessionStatePath string
	HomeDir          string // sandbox-private agent home, mounted as $HOME (isolated per sandbox)
	// DestGen is the sandbox directory's generation (sandbox.Base.Gen) — bumped
	// whenever the dir at Dest had to be created from nothing. It travels as a
	// guest env var, which folds it into the session hash: a session
	// created before a hand-deleted-and-recreated tree still shares the
	// DELETED directory, and the generation flip is what makes it read as stale
	// instead of silently reused. "" (a pre-gen sandbox) adds no flag, keeping
	// existing sessions' hashes unchanged.
	DestGen string
	// MountGen fingerprints the on-disk identity (device+inode) of the
	// individual source mounts in SrcMounts — the view directories of a narrowed
	// sandbox, and any adopted worktrees. Like DestGen it travels as a guest
	// env var folded into the session hash, and for the same reason: a
	// share is pinned to the inode it names, so a host-side git operation
	// (checkout, rebase) that removes and recreates a mounted directory leaves a
	// live session bound to the orphaned OLD inode — the agent reading stale
	// files and writing where nobody looks. When the fingerprint changes the
	// session hash flips, so the next enter/exec rebuilds against the fresh
	// directories. Empty for a sandbox with no individual mounts (the common
	// case: one managed source, no include, whose <slug>/ root mount is itself
	// inode-stable), keeping that argv — and its session hash — unchanged.
	MountGen string
	// MountIDs is the same identity material MountGen hashes, encoded for the
	// sandboxer.mounts label / machine record (sandbox.EncodeMountIDs). MountGen
	// makes a moved mount set read as stale; this makes it EXPLAINABLE — diffed
	// against a fresh resolve it names which directory appeared, which one the
	// host recreated under a live session's feet, and which one is gone, instead
	// of reporting every drift as the "profile changed" it usually is not.
	//
	// Deliberately absent from the hashed argv: it is recorded as identity
	// metadata only (msb labels, the host-side record), never hashed — MountGen
	// already carries this identity into the hash, and putting the same material
	// in twice would double-count it and flip every existing session's hash.
	MountIDs string
	// AuthEnv is the agents' auth environment ("KEY=value" entries, sorted by
	// the caller), collected by the CLI from the HOST environment when the
	// profile opts into hostConfigs: long-lived tokens like
	// CLAUDE_CODE_OAUTH_TOKEN (`claude setup-token`) or plain API keys. Env is
	// the sanctioned channel for these — unlike a copied OAuth credentials
	// FILE, whose rotating refresh chain dies (or hijacks the host's session)
	// on the next refresh either side performs. The profile's own env is
	// appended after it, so it still overrides per key.
	//
	// It is set on the PROCESS, never on the session machine: `run` bakes it
	// (the agent is that machine's workload) while a session shell gets
	// it per `exec`. Two reasons. It keeps credentials out of the long-lived
	// machine's inspectable configuration; and, decisively, it keeps them out
	// of the session hash — which fingerprints the create argv. A hash that
	// moved with a token value made every rotation, and every terminal that
	// happened not to export the var, read as "profile changed", so a session
	// went permanently stale from ambient shell state rather than from the
	// config. Each new shell picks up the current value with no rebuild at all.
	AuthEnv         []string
	RT              config.Runtime
	Profile         *config.Profile
	ProfileJSONPath string // staged into the per-slug run dir, shared ro at /run/sandboxer
	Interactive     bool
	NoEgress        bool   // SANDBOXER_NO_EGRESS
	Mem             string // memory cap (e.g. 2G); empty = the microVM default
	CPU             string // CPU cap (accepts a float or systemd "100%")
	Args            []string
	Stdin           io.Reader
	Stdout          io.Writer
	Stderr          io.Writer
}

// Run executes the sandbox's one-shot machine and returns its exit code.
func Run(o RunOpts) (int, error) {
	return vmRun(o), nil
}

// ImageExists reports whether the engine's image store holds the image —
// msb's own store, the one `create` boots from.
func ImageExists(_, image string) bool {
	return msbImageExists(image)
}

// ImageID returns the local content id for image, or "" on any failure. A
// locally absent image is "unknown", never an error: callers skip the
// image-freshness check on "" instead of failing before the image is built.
func ImageID(_, image string) string {
	return msbImageID(image)
}

// RemoveImage removes a local image by name/tag from the engine's store. An
// already-absent image is success — removal is idempotent — so only a real
// store failure errors.
func RemoveImage(_, image string) error {
	return msbRemoveImage(image)
}

// PullImage fetches image into msb's own store (`msb pull`, host-side — it
// honors the shell's HTTP(S)_PROXY), streaming the runner's progress through.
// This is the refresh path for a moved prebuilt `latest`: a create only pulls
// a ref MISSING from the store, so a nightly-republished default never
// reaches an already-cached host without it. The fresh digest then reads as
// stale against a live session's recorded one, and the next enter recreates
// the machine on the new rootfs.
func PullImage(_, image string, stdout, stderr io.Writer) error {
	cmd := exec.Command(msbBin(), "pull", image)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("msb pull %s: %w", image, err)
	}
	return nil
}

// exitCode maps a command error to a process exit code (0 success, the child's
// code for a non-zero exit, 1 for failure to start).
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}

// cpusFromQuota converts a CPU limit to a core count. It accepts a
// systemd-style quota ("100%", "150%") and converts it to a core count ("1",
// "1.5"); a plain value (already a core count like "1.5") is passed through. An
// empty or unparseable input yields "" (no limit).
func cpusFromQuota(s string) string {
	if s == "" {
		return ""
	}
	if pct, ok := strings.CutSuffix(s, "%"); ok {
		n, err := strconv.ParseFloat(pct, 64)
		if err != nil {
			return ""
		}
		return strconv.FormatFloat(n/100, 'f', -1, 64)
	}
	return s
}

// pathExists reports whether a host path exists.
func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// IsInteractiveTerminal reports whether v is a terminal a person could answer
// a prompt on — stricter than IsTerminal, which only asks for a character
// device (/dev/null is one). See isInteractiveTerminal.
func IsInteractiveTerminal(v any) bool {
	f, ok := v.(*os.File)
	return ok && isInteractiveTerminal(f)
}

func IsTerminal(v any) bool {
	f, ok := v.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
