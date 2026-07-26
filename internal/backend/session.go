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

	"github.com/irasikhin/sandboxer/internal/egress"
	"github.com/irasikhin/sandboxer/internal/execx"
)

// Labels stamped on persistent session containers so they can be discovered,
// matched and garbage-collected through engine queries alone — the engine's
// container store is the only session state, there is no extra metadata file.
const (
	// LabelManaged marks a container as created by sandboxer (value "true").
	LabelManaged = "sandboxer.managed"
	// LabelSlug records the sandbox slug the session belongs to.
	LabelSlug = "sandboxer.slug"
	// LabelBase records the host state dir the session was created from.
	LabelBase = "sandboxer.base"
	// LabelHash records the ConfigHash the session was created with; a mismatch
	// against the freshly computed hash means the desired configuration changed
	// and the running session is stale.
	LabelHash = "sandboxer.hash"
	// LabelMounts records the individual source mounts' on-disk identities the
	// session was created with (RunOpts.MountIDs). LabelHash says THAT the
	// desired configuration moved; diffing this against a fresh resolve says
	// what — which view directory appeared, which one a host-side checkout
	// recreated, which one is gone. Empty on sessions created before it existed,
	// which callers must treat as "unknown", never as "nothing was mounted".
	LabelMounts = "sandboxer.mounts"
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

// guestExec builds the command that runs argv non-interactively inside the
// already-running guest of the session named name, its stdout captured by the
// caller. It is the ONE engine-specific primitive the backend-neutral session
// machinery leans on — the tmux layout capture, the agent process listing and
// the idleness probe all reach into the guest the same way — so a second
// isolation backend needs only to teach this one helper its own "exec in the
// guest" shape, and every reader of guest state comes along for free. For the
// container engines that is `<engine> exec <name> <argv…>`; for the microVM
// backend it is `smolvm machine exec --name <name> -- <argv…>`, run through the
// resolved smolvm binary.
func guestExec(engine, name string, argv ...string) *exec.Cmd {
	if engine == smolvmEngine {
		args := append([]string{"machine", "exec", "--name", name, "--"}, argv...)
		return exec.Command(smolvmBin(), args...)
	}
	args := append([]string{"exec", name}, argv...)
	return exec.Command(engine, args...)
}

// createArgv assembles the engine argv that creates a detached persistent
// session container: the same isolation/mount/env flags as a one-shot run
// (commonArgs), but started with `run -d --init`, named and labeled for later
// discovery, and kept alive by `sleep infinity` instead of the agent command.
// Differences from runArgs are all deliberate: no --rm (the session outlives
// any single command and is removed explicitly) and no -i/-t (nothing attaches
// at create time — exec does). Mem/CPU limits still apply via commonArgs.
func createArgv(o RunOpts, egNet, egProxyURL, name, hash string) []string {
	args := []string{
		"run", "-d", "--init", "--name", name,
		"--label", LabelManaged + "=true",
		"--label", LabelSlug + "=" + o.Slug,
		"--label", LabelBase + "=" + o.BaseDir,
		"--label", LabelHash + "=" + hash,
		"--label", LabelMounts + "=" + o.MountIDs,
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
	if IsTerminal(o.Stdin) && IsTerminal(o.Stdout) {
		args = append(args, "-t")
	}
	args = append(args, "-w", o.Dest)
	if term := os.Getenv("TERM"); term != "" {
		args = append(args, "--env", "TERM="+term)
	}
	// Auth env travels per exec, not baked into the container (RunOpts.AuthEnv):
	// every shell gets the token the host has RIGHT NOW, so a rotation reaches
	// the sandbox without rebuilding the session — and the session's own
	// environment never holds a credential.
	args = append(args, authEnvArgs(o)...)
	args = append(args, name)
	args = append(args, cmdArgs...)
	return args
}

// ConfigHash fingerprints the session's create configuration: a sha256 over
// the canonical create argv EXCLUDING the container name and labels, so
// renaming or relabeling a session never flips the hash, while any change to
// the image, mounts, env, proxies or resource limits does. Auth env is absent
// by construction — it is never part of the create argv (RunOpts.AuthEnv) —
// so a rotated token, or a terminal that does not export one, no longer reads
// as a changed profile. The result is
// stored in the LabelHash label and compared on re-enter to decide whether the
// running session still matches the desired configuration.
//
// When egress is active (egNet != "") the generated squid.conf's fingerprint is
// folded in too, so editing the domains, the proxy or the routes reconfigures
// the session (recreate) instead of silently taking effect only after a manual
// recreate. compose passes egNet="" and stays fingerprint-free (already
// documented stale — the dynamic egress flags are shown as a note, not run).
func ConfigHash(o RunOpts, egNet, egProxyURL string) string {
	core := []string{"run", "-d", "--init"}
	core = append(core, commonArgs(o, egNet, egProxyURL)...)
	core = append(core, o.Image, "sleep", "infinity")
	// NUL-joined so adjacent argv elements can never collide by concatenation.
	joined := strings.Join(core, "\x00")
	if egNet != "" {
		joined += "\x00" + egress.ConfFingerprint(o.RT.Domains, ContainerProxyURL(o.RT.Proxy), containerRoutes(o.RT.Routes))
	}
	sum := sha256.Sum256([]byte(joined))
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

// SessionWantHash computes the ConfigHash a fresh persistent session for o
// must carry: hashed with the session's STABLE egress identifiers (purely
// name-derived via Lookup, no per-run randomness), so the hash computed on a
// re-enter always matches the one stamped at create time — and toggling
// egress on/off flips it, because the create argv genuinely changes
// (--network + proxy env). The single staleness oracle for EnsureSession and
// the CLI's exec routing — they must never disagree.
func SessionWantHash(o RunOpts) string {
	if o.Engine == smolvmEngine {
		return vmSessionWantHash(o)
	}
	egNet, egProxyURL := "", ""
	if egressRequired(o) {
		lk := egress.Lookup(o.Engine, SessionName(o.Slug, o.BaseDir))
		egNet, egProxyURL = lk.Net(), lk.ProxyURL()
	}
	return ConfigHash(o, egNet, egProxyURL)
}

// InspectSession reads the session container's running state, recorded config
// hash and image ID in one `container inspect` call. A non-zero exit means
// the container does not exist: the zero SessionInfo.
func InspectSession(engine, name string) SessionInfo {
	if engine == smolvmEngine {
		return vmInspectSession(name)
	}
	out, err := exec.Command(engine, "container", "inspect", "--format",
		`{{.State.Running}} {{index .Config.Labels "`+LabelHash+`"}} {{.Image}} {{index .Config.Labels "`+LabelMounts+`"}}`, name).Output()
	if err != nil {
		return SessionInfo{}
	}
	info := SessionInfo{Exists: true}
	// Single-space split, NOT strings.Fields: a missing hash label renders as
	// an empty middle field that must keep its slot, or the trailing image ID
	// would shift into the hash position. The mounts label goes LAST and is
	// base64url by construction (sandbox.EncodeMountIDs) — no space can appear
	// inside it, so it cannot shift anything either; when it is missing,
	// TrimSpace drops the trailing separator and the field simply is not there.
	fields := strings.Split(strings.TrimSpace(string(out)), " ")
	info.Running = fields[0] == "true"
	if len(fields) > 1 {
		info.Hash = fields[1]
	}
	if len(fields) > 2 {
		// Docker reports the container's image as "sha256:<hex>", podman as
		// bare hex — normalize so ImageFresh compares like with like.
		info.ImageID = strings.TrimPrefix(fields[2], "sha256:")
	}
	if len(fields) > 3 {
		info.Mounts = fields[3]
	}
	return info
}

// SessionIdle reports whether the session container holds NOTHING worth
// preserving: its in-container tmux server (the one `enter` attaches, socket
// "sandboxer") runs no session at all. It is the deciding fact when a running
// session's configuration went stale — an idle one can be converged on the
// spot, a busy one holds the user's agent and must be attached instead.
//
// Idleness is a POSITIVE finding, never an assumption: only a clean listing
// that came back empty, or tmux itself reporting no server, counts. Every
// other outcome (engine error, no tmux in the image, an unreadable container)
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

// EnsureSession converges slug's persistent session container to a running,
// configuration-fresh state and returns its name, ready for ExecSession. It
// is the thin driver around the pure planSession policy: one inspect, one
// plan, one action. The engine's container store is the only session state,
// so re-running EnsureSession after any outcome is always safe.
func EnsureSession(o RunOpts) (string, error) {
	if o.Engine == smolvmEngine {
		return vmEnsureSession(o)
	}
	name := SessionName(o.Slug, o.BaseDir)

	// Serialize concurrent converges of THIS session across processes. Two
	// first-enters racing to create the same session would otherwise each bring
	// up the egress sidecar, and the loser's egress.UpNamed would tear down the
	// winner's live proxy (it removes same-named resources as presumed
	// leftovers), leaving the winner's session with no outbound until its next
	// enter. Under the lock the loser instead re-inspects, finds the winner's
	// running-and-fresh session, and execs it. Per-session-name, so different
	// sandboxes never contend. Best-effort: an unlockable path proceeds without
	// the lock, exactly as before this guard.
	if o.BaseDir != "" {
		if err := os.MkdirAll(o.BaseDir, 0o700); err == nil {
			if release, lerr := lockFile(filepath.Join(o.BaseDir, "."+name+".lock")); lerr == nil {
				defer release()
			}
		}
	}

	needEgress := egressRequired(o)
	if needEgress && len(o.RT.Domains) == 0 {
		return "", errEmptyAllowlist
	}
	hash := SessionWantHash(o)

	info := InspectSession(o.Engine, name)
	// The wanted image ID only matters for an existing container, and it is
	// read from whatever is locally present RIGHT NOW: createSession's
	// ensureImage builds a missing image later, so an absent image yields ""
	// here and the freshness check is skipped — never a false "stale".
	wantImage := ""
	if info.Exists {
		wantImage = ImageID(o.Engine, o.Image)
	}

	switch action := planSession(info, hash, wantImage); action {
	case actExec, actStart:
		// Adoption health-check: a configuration-fresh container whose egress
		// proxy died or vanished has no outbound path — treat it as stale and
		// rebuild both together (announced, like any recreate).
		if needEgress && !reviveEgress(o.Engine, name, action) {
			notice(o.Stderr, "recreating session: egress proxy is gone")
			return recreateSession(o, name, hash)
		}
		if action == actExec {
			return name, nil
		}
		if err := execx.Run(o.Engine, "start", name); err != nil {
			return "", fmt.Errorf("start session %s: %w", name, err)
		}
		return name, nil
	case actRecreate:
		notice(o.Stderr, "recreating session: "+staleReason(info, hash))
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
	// The container is about to be replaced, taking its in-container tmux — and
	// the user's windows — with it. Save the layout first, while it still runs,
	// so the next attach restores it: a mount change no longer costs a session.
	if SaveSessionState(o.Engine, name, o.SessionStatePath) {
		notice(o.Stderr, "saved your session layout — restoring it on attach "+
			"(recorded agents relaunch and resume; other running programs are interrupted)")
	}
	// Announced: on a wedged engine `rm -f` can take a long time, and silence
	// here reads as a hang.
	notice(o.Stderr, "removing the old session container…")
	if err := execx.Run(o.Engine, "rm", "-f", name); err != nil {
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
		if err := ensureProxyImage(o); err != nil {
			return "", err
		}
		notice(o.Stderr, "starting the egress sidecar…")
		e, err := egress.UpNamed(o.Engine, name, o.RT.Domains, ContainerProxyURL(o.RT.Proxy), containerRoutes(o.RT.Routes), o.BaseDir, o.Stderr)
		if err != nil {
			return "", fmt.Errorf("egress allowlist proxy failed to start: %w — "+
				"refusing to run on an open network (disable with egress.enabled = false or SANDBOXER_NO_EGRESS=1)", err)
		}
		eg = e
		egNet, egProxyURL = eg.Net(), eg.ProxyURL()
	} else {
		// Sweep egress leftovers from a previous egress-enabled life of this
		// session (idempotent no-op when there are none).
		egress.Lookup(o.Engine, name).Down()
	}
	notice(o.Stderr, "creating the session container…")
	cmd := exec.Command(o.Engine, createArgv(o, egNet, egProxyURL, name, hash)...)
	cmd.Stdout = io.Discard // `run -d` prints the new container id
	cmd.Stderr = o.Stderr
	if err := cmd.Run(); err != nil {
		// Two concurrent first enters race to this create under the same
		// deterministic name; the loser fails on the duplicate. Adopt the
		// winner's container when it is running and configuration-fresh (our
		// stably-named egress, when required, serves it just as well) instead
		// of surfacing the name conflict.
		if again := InspectSession(o.Engine, name); again.Exists && again.Running && again.Hash == hash {
			return name, nil
		}
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
	if o.Engine == smolvmEngine {
		return vmExecSession(o, name, cmdArgs)
	}
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
	if engine == smolvmEngine {
		return vmStopSession(slug, baseDir)
	}
	name := SessionName(slug, baseDir)
	if InspectSession(engine, name).Exists {
		if err := execx.Run(engine, "stop", name); err != nil {
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
	if engine == smolvmEngine {
		return vmRemoveSession(slug, baseDir)
	}
	return removeSessionByName(engine, SessionName(slug, baseDir))
}

func removeSessionByName(engine, name string) error {
	if InspectSession(engine, name).Exists {
		if err := execx.Run(engine, "rm", "-f", name); err != nil {
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
	if engine == smolvmEngine {
		return vmRemoveAllSessions(baseDir)
	}
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
	out, err := execx.Output(engine, "ps", "-a",
		"--filter", "label="+LabelManaged+"=true",
		"--filter", "label="+LabelBase+"="+baseDir,
		"--format", "{{.Names}}")
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	return strings.Fields(out), nil // container names contain no whitespace
}

// SessionStates maps each of baseDir's session slugs to its container status
// ("running", "exited", …) for listings. Two engine calls: `ps` enumerates the
// names, then one batched `container inspect` reads slug + status — podman and
// docker disagree on label templating in `ps --format` (`index .Labels` vs
// `.Label`), while the full inspect template works verbatim on both.
func SessionStates(engine, baseDir string) (map[string]string, error) {
	if engine == smolvmEngine {
		return vmSessionStates(baseDir)
	}
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
	out, err := execx.Output(engine, args...)
	if err != nil {
		return nil, fmt.Errorf("inspect sessions: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
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
	if engine == smolvmEngine {
		return vmOrphanSessions()
	}
	out, err := execx.Output(engine, "ps", "-a",
		"--filter", "label="+LabelManaged+"=true",
		"--format", "{{.Names}}")
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	names := strings.Fields(out)
	if len(names) == 0 {
		return nil, nil
	}
	// One batched inspect: the base label per line, in argument order. Only the
	// trailing newline is trimmed — a missing label is an EMPTY line that must
	// keep its slot, or every base after it would shift onto the wrong name.
	args := []string{"container", "inspect", "--format",
		`{{index .Config.Labels "` + LabelBase + `"}}`}
	args = append(args, names...)
	bout, err := execx.Output(engine, args...)
	if err != nil {
		return nil, fmt.Errorf("inspect sessions: %w", err)
	}
	bases := strings.Split(strings.TrimRight(bout, "\n"), "\n")
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
