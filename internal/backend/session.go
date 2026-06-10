package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/irasikhin/sandboxer/internal/egress"
)

// Labels stamped on persistent session containers so they can be discovered,
// matched and garbage-collected through engine queries alone — the engine's
// container store is the only session state, there is no extra metadata file.
const (
	// LabelManaged marks a container as created by sandboxer (value "true").
	LabelManaged = "sandboxer.managed"
	// LabelSlug records the sandbox slug the session belongs to.
	LabelSlug = "sandboxer.slug"
	// LabelBase records the host .sandboxer base dir the session was created from.
	LabelBase = "sandboxer.base"
	// LabelHash records the ConfigHash the session was created with; a mismatch
	// against the freshly computed hash means the desired configuration changed
	// and the running session is stale.
	LabelHash = "sandboxer.hash"
)

// SessionName returns the deterministic container name for slug's persistent
// session under baseDir: "sandboxer-<slug>-<hash>". The slug is sanitized to
// the container-name alphabet both engines accept, and the 8-hex sha256 prefix
// of baseDir disambiguates same-named sandboxes living in different projects.
func SessionName(slug, baseDir string) string {
	return "sandboxer-" + sanitizeContainerName(slug) + "-" + shortHash(baseDir)
}

// sanitizeContainerName maps s onto the container-name alphabet shared by
// podman and docker ([a-zA-Z0-9_.-]); every other rune becomes '-'. The
// stricter leading-character rule is satisfied by SessionName's prefix.
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

// createArgv assembles the engine argv that creates a detached persistent
// session container: the same isolation/mount/env flags as a one-shot run
// (commonArgs), but started with `run -d --init`, named and labeled for later
// discovery, and kept alive by `sleep infinity` instead of the agent command.
// Differences from runArgs are all deliberate: no --rm (the session outlives
// any single command and is removed explicitly), no -i/-t (nothing attaches at
// create time — exec does), and no `timeout` wrapper: a persistent session has
// no wall clock — it idles between exec'd commands, so o.Wall bounds nothing
// meaningful here and is ignored. Mem/CPU limits still apply via commonArgs.
func createArgv(o RunOpts, egNet, egProxyURL, name, hash string) []string {
	args := []string{
		"run", "-d", "--init", "--name", name,
		"--label", LabelManaged + "=true",
		"--label", LabelSlug + "=" + o.Slug,
		"--label", LabelBase + "=" + o.BaseDir,
		"--label", LabelHash + "=" + hash,
	}
	args = append(args, commonArgs(o, egNet, egProxyURL)...)
	args = append(args, o.Image, "sleep", "infinity")
	return args
}

// execArgv assembles the engine argv that runs cmdArgs inside the session
// container name. -t only with a real TTY (same rule as runArgs); -w pins the
// workdir to the sandbox copy; TERM is forwarded when set so full-screen TUIs
// render correctly (exec does not inherit the caller's terminal environment).
func execArgv(o RunOpts, name string, cmdArgs []string) []string {
	args := []string{"exec", "-i"}
	if isTerminal(o.Stdin) && isTerminal(o.Stdout) {
		args = append(args, "-t")
	}
	args = append(args, "-w", o.Dest)
	if term := os.Getenv("TERM"); term != "" {
		args = append(args, "--env", "TERM="+term)
	}
	args = append(args, name)
	args = append(args, cmdArgs...)
	return args
}

// ConfigHash fingerprints the session's create configuration: a sha256 over
// the canonical create argv EXCLUDING the container name and labels, so
// renaming or relabeling a session never flips the hash, while any change to
// the image, mounts, env, proxies or resource limits does. The result is
// stored in the LabelHash label and compared on re-enter to decide whether the
// running session still matches the desired configuration.
func ConfigHash(o RunOpts, egNet, egProxyURL string) string {
	core := []string{"run", "-d", "--init"}
	core = append(core, commonArgs(o, egNet, egProxyURL)...)
	core = append(core, o.Image, "sleep", "infinity")
	// NUL-joined so adjacent argv elements can never collide by concatenation.
	sum := sha256.Sum256([]byte(strings.Join(core, "\x00")))
	return hex.EncodeToString(sum[:])
}

// CreateArgv returns the engine argv that would create the persistent session
// container for o, without an active egress sidecar (the allowlist proxy is
// created dynamically at run time) — the exported seam parallel to RunArgv.
func CreateArgv(o RunOpts, name, hash string) []string {
	return createArgv(o, "", "", name, hash)
}

// ExecArgv returns the engine argv that runs cmdArgs inside the session
// container name — the exported seam parallel to RunArgv.
func ExecArgv(o RunOpts, name string, cmdArgs []string) []string {
	return execArgv(o, name, cmdArgs)
}

// --- session lifecycle -------------------------------------------------------

// errEmptyAllowlist rejects an egress-required run with nothing on the
// allowlist: that is always a misconfiguration, never a "block everything"
// mode. Shared by the one-shot Run and the persistent-session paths so both
// fail closed with the same guidance.
var errEmptyAllowlist = errors.New("egress allowlist is enabled but no domains are allowed — " +
	"set --allow-domains / network.allowedDomains, or disable egress " +
	"(egress: false, or SANDBOXER_NO_EGRESS=1)")

// egressRequired reports whether o must run behind the egress allowlist
// sidecar: not explicitly disabled (NoEgress / egress: false) and no upstream
// corporate proxy already holding the boundary. The single policy predicate
// for Run and the session lifecycle — they must never disagree, because the
// session ConfigHash depends on it.
func egressRequired(o RunOpts) bool {
	return !o.NoEgress && o.RT.Egress && o.RT.HTTPProxy == "" && o.RT.HTTPSProxy == ""
}

// SessionWantHash computes the ConfigHash a fresh persistent session for o
// must carry: hashed with the session's STABLE egress identifiers (purely
// name-derived via Lookup, no per-run randomness), so the hash computed on a
// re-enter always matches the one stamped at create time — and toggling
// egress on/off flips it, because the create argv genuinely changes
// (--network + proxy env). The single staleness oracle for EnsureSession and
// the CLI's exec routing — they must never disagree.
func SessionWantHash(o RunOpts) string {
	egNet, egProxyURL := "", ""
	if egressRequired(o) {
		lk := egress.Lookup(o.Engine, SessionName(o.Slug, o.BaseDir))
		egNet, egProxyURL = lk.Net(), lk.ProxyURL()
	}
	return ConfigHash(o, egNet, egProxyURL)
}

// SessionInfo is what a single engine inspect reveals about a session
// container: whether it exists at all, whether it is currently running, and
// the ConfigHash it was created with (from the LabelHash label; "" when the
// label is missing, which compares as stale against any wanted hash).
type SessionInfo struct {
	Exists  bool
	Running bool
	Hash    string
}

// InspectSession reads the session container's running state and recorded
// config hash in one `container inspect` call. A non-zero exit means the
// container does not exist: the zero SessionInfo.
func InspectSession(engine, name string) SessionInfo {
	out, err := exec.Command(engine, "container", "inspect", "--format",
		`{{.State.Running}} {{index .Config.Labels "`+LabelHash+`"}}`, name).Output()
	if err != nil {
		return SessionInfo{}
	}
	info := SessionInfo{Exists: true}
	fields := strings.Fields(string(out))
	if len(fields) > 0 {
		info.Running = fields[0] == "true"
	}
	if len(fields) > 1 {
		info.Hash = fields[1]
	}
	return info
}

// sessionIdle reports whether no client is attached to the session's shared
// in-container tmux server (`tmux -L sandboxer`). Best-effort BY DESIGN: it
// only sees clients that attached through that tmux server — a raw
// `podman exec` shell is invisible to it — so "idle" is an informed guess used
// to choose between recreate and refuse, not a lock. Any error (no tmux server
// started yet, engine hiccup) or an empty client list counts as idle.
func sessionIdle(engine, name string) bool {
	out, err := exec.Command(engine, "exec", name, "tmux", "-L", "sandboxer", "list-clients").Output()
	if err != nil {
		return true
	}
	return strings.TrimSpace(string(out)) == ""
}

// sessionAction is planSession's verdict on how to converge a session.
type sessionAction int

const (
	actCreate   sessionAction = iota // no container — create one
	actStart                         // stopped container, fresh config — start it
	actExec                          // running container, fresh config — use as-is
	actRecreate                      // config changed and nobody attached — replace it
	actRefuse                        // config changed but the session is busy — error out
)

// planSession is the pure session-lifecycle policy: given what exists (info),
// what the caller wants (wantHash) and whether anyone is attached (idle), it
// picks the one action that converges the session. Engine-free so the whole
// decision table is unit-testable:
//
//	not found               → create
//	stopped + fresh         → start
//	stopped + stale         → recreate
//	running + fresh         → exec
//	running + stale + idle  → recreate
//	running + stale + busy  → refuse
func planSession(info SessionInfo, wantHash string, idle bool) sessionAction {
	switch {
	case !info.Exists:
		return actCreate
	case !info.Running:
		if info.Hash == wantHash {
			return actStart
		}
		return actRecreate
	case info.Hash == wantHash:
		return actExec
	case idle:
		return actRecreate
	default:
		return actRefuse
	}
}

// EnsureSession converges slug's persistent session container to a running,
// configuration-fresh state and returns its name, ready for ExecSession. It
// is the thin driver around the pure planSession policy: one inspect, one
// plan, one action. The engine's container store is the only session state,
// so re-running EnsureSession after any outcome is always safe.
func EnsureSession(o RunOpts) (string, error) {
	name := SessionName(o.Slug, o.BaseDir)

	needEgress := egressRequired(o)
	if needEgress && len(o.RT.Domains) == 0 {
		return "", errEmptyAllowlist
	}
	hash := SessionWantHash(o)

	info := InspectSession(o.Engine, name)
	// Idleness only matters on the running+stale branch; skip the engine call
	// otherwise.
	idle := false
	if info.Running && info.Hash != hash {
		idle = sessionIdle(o.Engine, name)
	}

	switch action := planSession(info, hash, idle); action {
	case actExec, actStart:
		// Adoption health-check: a configuration-fresh container whose egress
		// proxy died or vanished has no outbound path — treat it as stale and
		// rebuild both together.
		if needEgress && !reviveEgress(o.Engine, name, action) {
			notice(o.Stderr, "recreating session: egress proxy is gone")
			return recreateSession(o, name, hash)
		}
		if action == actExec {
			return name, nil
		}
		if err := exec.Command(o.Engine, "start", name).Run(); err != nil {
			return "", fmt.Errorf("start session %s: %w", name, err)
		}
		return name, nil
	case actRefuse:
		return "", fmt.Errorf("session %s: the profile changed but other clients are attached — "+
			"detach them first, or rerun with --ephemeral", name)
	case actRecreate:
		notice(o.Stderr, "recreating session: profile changed")
		return recreateSession(o, name, hash)
	default: // actCreate
		return createSession(o, name, hash)
	}
}

// reviveEgress brings the session's egress back for an adopted container: a
// to-be-exec'd session needs its proxy already running, a to-be-started one
// gets its proxy (stopped alongside the session) started again — a start on an
// already-running proxy is a harmless no-op. It reports false when the proxy
// is gone, signaling the caller to recreate the whole session.
func reviveEgress(engine, name string, action sessionAction) bool {
	lk := egress.Lookup(engine, name)
	if action == actExec {
		return lk.ProxyRunning()
	}
	return lk.Start() == nil
}

// recreateSession replaces a stale session: remove the old container, then
// create anew. Stale egress resources are swept inside createSession (UpNamed
// pre-cleans; the no-egress path Downs leftovers), after the container — which
// may still be attached to the old network — is gone.
func recreateSession(o RunOpts, name, hash string) (string, error) {
	if err := exec.Command(o.Engine, "rm", "-f", name).Run(); err != nil {
		return "", fmt.Errorf("remove stale session %s: %w", name, err)
	}
	return createSession(o, name, hash)
}

// createSession creates the detached session container, bringing up its
// stably-named egress sidecar first when the allowlist is required — with the
// same fail-closed policy as Run: never fall back to an open network. The
// image is ensured before anything else because the sidecar runs on it too.
func createSession(o RunOpts, name, hash string) (string, error) {
	if err := ensureImage(o); err != nil {
		return "", err
	}
	var eg *egress.Egress
	egNet, egProxyURL := "", ""
	if egressRequired(o) {
		e, err := egress.UpNamed(o.Engine, o.Image, name, o.RT.Domains, o.RT.UpstreamProxy, o.Stderr)
		if err != nil {
			return "", fmt.Errorf("egress allowlist proxy failed to start: %w — "+
				"refusing to run on an open network (disable with egress: false or SANDBOXER_NO_EGRESS=1)", err)
		}
		eg = e
		egNet, egProxyURL = eg.Net(), eg.ProxyURL()
	} else {
		// Sweep egress leftovers from a previous egress-enabled life of this
		// session (idempotent no-op when there are none).
		egress.Lookup(o.Engine, name).Down()
	}
	cmd := exec.Command(o.Engine, createArgv(o, egNet, egProxyURL, name, hash)...)
	cmd.Stdout = io.Discard // `run -d` prints the new container id
	cmd.Stderr = o.Stderr
	if err := cmd.Run(); err != nil {
		// Don't leave a proxy running for a container that never came up.
		eg.Down()
		return "", fmt.Errorf("create session %s: %w", name, err)
	}
	return name, nil
}

// ExecSession runs cmdArgs inside the (already ensured) session container and
// returns the command's exit code — the persistent-session counterpart of Run,
// with the same stdio wiring. Proxy and credential env were baked into the
// container at create time; only the stdio and TERM travel with each exec.
func ExecSession(o RunOpts, name string, cmdArgs []string) (int, error) {
	cmd := exec.Command(o.Engine, execArgv(o, name, cmdArgs)...)
	cmd.Stdin = o.Stdin
	cmd.Stdout = o.Stdout
	cmd.Stderr = o.Stderr
	return exitCode(cmd.Run()), nil
}

// StopSession stops slug's session container and its egress proxy, keeping
// the container, the egress networks and the sandbox files in place so a later
// EnsureSession resumes them with a plain start. Idempotent: a missing or
// already-stopped session is not an error.
func StopSession(engine, slug, baseDir string) error {
	name := SessionName(slug, baseDir)
	if InspectSession(engine, name).Exists {
		if err := exec.Command(engine, "stop", name).Run(); err != nil {
			return fmt.Errorf("stop session %s: %w", name, err)
		}
	}
	// Only a running proxy is stopped — a session without egress has none.
	if lk := egress.Lookup(engine, name); lk.ProxyRunning() {
		if err := lk.Stop(); err != nil {
			return err
		}
	}
	return nil
}

// RemoveSession removes slug's session container and tears down its egress
// resources entirely. Idempotent: a missing session only sweeps egress.
func RemoveSession(engine, slug, baseDir string) error {
	return removeSessionByName(engine, SessionName(slug, baseDir))
}

func removeSessionByName(engine, name string) error {
	if InspectSession(engine, name).Exists {
		if err := exec.Command(engine, "rm", "-f", name).Run(); err != nil {
			return fmt.Errorf("remove session %s: %w", name, err)
		}
	}
	egress.Lookup(engine, name).Down() // idempotent, best-effort
	return nil
}

// RemoveAllSessions removes every sandboxer-managed session container created
// from baseDir, each with its egress resources. Failures are collected so one
// stubborn session does not strand the rest.
func RemoveAllSessions(engine, baseDir string) error {
	names, err := sessionNames(engine, baseDir)
	if err != nil {
		return err
	}
	var errs []error
	for _, n := range names {
		errs = append(errs, removeSessionByName(engine, n))
	}
	return errors.Join(errs...)
}

// sessionNames lists the names of every sandboxer-managed session container
// created from baseDir, via the labels stamped at create time.
func sessionNames(engine, baseDir string) ([]string, error) {
	out, err := exec.Command(engine, "ps", "-a",
		"--filter", "label="+LabelManaged+"=true",
		"--filter", "label="+LabelBase+"="+baseDir,
		"--format", "{{.Names}}").Output()
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	return strings.Fields(string(out)), nil // container names contain no whitespace
}

// SessionStates maps each of baseDir's session slugs to its container status
// ("running", "exited", …) for listings. Two engine calls: `ps` enumerates the
// names, then one batched `container inspect` reads slug + status — podman and
// docker disagree on label templating in `ps --format` (`index .Labels` vs
// `.Label`), while the full inspect template works verbatim on both.
func SessionStates(engine, baseDir string) (map[string]string, error) {
	names, err := sessionNames(engine, baseDir)
	if err != nil {
		return nil, err
	}
	states := make(map[string]string, len(names))
	if len(names) == 0 {
		return states, nil
	}
	args := []string{"container", "inspect", "--format",
		`{{.State.Status}} {{index .Config.Labels "` + LabelSlug + `"}}`}
	args = append(args, names...)
	out, err := exec.Command(engine, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("inspect sessions: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		// Status first: it never contains a space, while a raw slug may.
		status, slug, ok := strings.Cut(line, " ")
		if !ok || slug == "" {
			continue
		}
		states[slug] = status
	}
	return states, nil
}

// OrphanSessions returns the names of sandboxer-managed session containers
// whose recorded base directory no longer exists on this host — the project
// was deleted behind the engine's back (rm -rf instead of `sandboxer rm`), so
// nothing will ever match them again. Reported by doctor with a removal hint;
// sandboxer itself never auto-removes them.
func OrphanSessions(engine string) ([]string, error) {
	out, err := exec.Command(engine, "ps", "-a",
		"--filter", "label="+LabelManaged+"=true",
		"--format", "{{.Names}}").Output()
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	names := strings.Fields(string(out))
	if len(names) == 0 {
		return nil, nil
	}
	// One batched inspect: the base label per line, in argument order. Only the
	// trailing newline is trimmed — a missing label is an EMPTY line that must
	// keep its slot, or every base after it would shift onto the wrong name.
	args := []string{"container", "inspect", "--format",
		`{{index .Config.Labels "` + LabelBase + `"}}`}
	args = append(args, names...)
	bout, err := exec.Command(engine, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("inspect sessions: %w", err)
	}
	bases := strings.Split(strings.TrimRight(string(bout), "\n"), "\n")
	var orphans []string
	for i, name := range names {
		if i >= len(bases) {
			break
		}
		base := strings.TrimSpace(bases[i])
		if base == "" {
			continue // no base label — not provably orphaned
		}
		if _, err := os.Stat(base); os.IsNotExist(err) {
			orphans = append(orphans, name)
		}
	}
	return orphans, nil
}

// notice prints a one-line lifecycle notice to the user's stderr (nil-safe).
func notice(w io.Writer, msg string) {
	if w != nil {
		fmt.Fprintf(w, "sandboxer: %s\n", msg)
	}
}
