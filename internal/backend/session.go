package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
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
