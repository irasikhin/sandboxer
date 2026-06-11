// Package sandbox owns the on-disk state under .sandboxer/: the base metadata
// (run.env), per-sandbox directories, dependency manifests and logs.
//
// A sandbox is driven entirely by `srcs`: nothing is copied by default. The
// sandbox directory holds exactly the entries listed in the profile's srcs
// (pulled in via the srcs package); an empty srcs means an empty sandbox.
// Changes to read-write entries are pushed back to their origins. No git is
// involved.
package sandbox

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/srcs"
)

// Base is a resolved project root and its .sandboxer state.
type Base struct {
	Src     string // absolute project root
	Dir     string // <Src>/.sandboxer
	Domains string
	Model   string
}

// ResolveBase resolves src to an absolute path, ensures the state dirs exist,
// seeds run.env on first use, and loads it.
func ResolveBase(src string) (*Base, error) {
	abs, err := filepath.Abs(src)
	if err != nil {
		return nil, fmt.Errorf("no such directory: %s", src)
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("no such directory: %s", src)
	}
	b := &Base{Src: abs, Dir: filepath.Join(abs, config.StateDirName)}
	if err := os.MkdirAll(b.metaDir(), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(b.logsDir(), 0o755); err != nil {
		return nil, err
	}
	// The state tree holds working copies and per-sandbox agent homes (which can
	// store login tokens after an in-sandbox `claude login`). Drop an allowlisting
	// .gitignore so none of it can be committed into the user's repo by accident —
	// only the three committed sandboxer-owned files (the .gitignore itself, the
	// project profile and the image hook) are un-ignored; _meta/_home/_logs/<slug>
	// stay ignored by the leading "*", which is the credential-leak guard.
	if err := ensureGitignore(filepath.Join(b.Dir, ".gitignore")); err != nil {
		return nil, err
	}
	runEnv := filepath.Join(b.metaDir(), "run.env")
	if _, err := os.Stat(runEnv); err != nil {
		d := config.LoadDefaults()
		content := fmt.Sprintf("SRC=%s\nMODEL=%s\nDOMAINS=%s\n", abs, d.Model, d.Domains)
		if err := os.WriteFile(runEnv, []byte(content), 0o644); err != nil {
			return nil, err
		}
		if err := os.WriteFile(b.AgentsListPath(), nil, 0o644); err != nil {
			return nil, err
		}
	}
	env, err := parseEnvFile(runEnv)
	if err != nil {
		return nil, err
	}
	b.Domains = env["DOMAINS"]
	b.Model = env["MODEL"]
	return b, nil
}

// OpenBase locates an existing project base read-only: it neither creates the
// state dirs nor seeds run.env, so it is safe to call on a `cd` (e.g. the direnv
// hook) without any side effects. It returns (nil, nil) when src has no
// initialized .sandboxer state — the caller treats that as "not a sandboxer
// project". Any run.env that is present is loaded for Domains/Model.
func OpenBase(src string) (*Base, error) {
	abs, err := filepath.Abs(src)
	if err != nil {
		return nil, err
	}
	if !RunEnvExists(abs) {
		return nil, nil
	}
	b := &Base{Src: abs, Dir: filepath.Join(abs, config.StateDirName)}
	if env, err := parseEnvFile(filepath.Join(b.metaDir(), "run.env")); err == nil {
		b.Domains = env["DOMAINS"]
		b.Model = env["MODEL"]
	}
	return b, nil
}

func (b *Base) metaDir() string  { return filepath.Join(b.Dir, "_meta") }
func (b *Base) logsDir() string  { return filepath.Join(b.Dir, "_logs") }
func (b *Base) homeRoot() string { return filepath.Join(b.Dir, "_home") }

// SandboxDir is the directory for a sandbox.
func (b *Base) SandboxDir(slug string) string { return filepath.Join(b.Dir, slug) }

// HomeDir is the sandbox's private agent home, mounted as $HOME in the
// container. It is isolated per sandbox so an agent authenticates inside its own
// sandbox and nothing from the host's real ~/.claude (history, tokens, MCP
// config) is ever pulled in, and so parallel sandboxes never race on one shared
// config file. It lives outside SandboxDir so it stays out of the agent's
// workdir and is never pushed back to a dependency origin.
func (b *Base) HomeDir(slug string) string { return filepath.Join(b.homeRoot(), slug) }

// EnsureHome creates the sandbox's private agent home if it is missing (0700, so
// only the owner can read the stored credentials). It is idempotent: safe to
// call on every enter/exec, including for sandboxes created before this existed.
func (b *Base) EnsureHome(slug string) error {
	return os.MkdirAll(b.HomeDir(slug), 0o700)
}

// ProfileJSONPath, ManifestPath, MetaFilePath locate per-sandbox metadata files.
func (b *Base) ProfileJSONPath(slug string) string {
	return filepath.Join(b.metaDir(), slug+".profile.json")
}
func (b *Base) ManifestPath(slug string) string {
	return filepath.Join(b.metaDir(), slug+".manifest.json")
}
func (b *Base) MetaFilePath(slug string) string {
	return filepath.Join(b.metaDir(), slug+".meta")
}
func (b *Base) LogPath(slug, ext string) string {
	return filepath.Join(b.logsDir(), slug+"."+ext)
}
func (b *Base) AgentsListPath() string { return filepath.Join(b.metaDir(), "agents.list") }
func (b *Base) currentPath() string    { return filepath.Join(b.metaDir(), "current") }
func (b *Base) setupStampPath(slug string) string {
	return filepath.Join(b.metaDir(), slug+".setup")
}

// SetupPending reports whether the profile's one-time setup script still needs
// to run for slug — never run before, or changed since the last run — and
// returns the script's hash to hand to MarkSetupDone. An empty script never
// pends. The hash makes the gate re-trigger when the setup script is edited.
func (b *Base) SetupPending(slug, script string) (bool, string) {
	if strings.TrimSpace(script) == "" {
		return false, ""
	}
	sum := sha256.Sum256([]byte(script))
	h := hex.EncodeToString(sum[:])
	prev, err := os.ReadFile(b.setupStampPath(slug))
	if err == nil && strings.TrimSpace(string(prev)) == h {
		return false, h
	}
	return true, h
}

// MarkSetupDone records that the setup script with the given hash completed, so
// it is not re-run until the script changes.
func (b *Base) MarkSetupDone(slug, hash string) error {
	return os.WriteFile(b.setupStampPath(slug), []byte(hash+"\n"), 0o644)
}

// RunEnvExists reports whether the base has been initialized (at least one
// sandbox created here).
func RunEnvExists(src string) bool {
	abs, err := filepath.Abs(src)
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(abs, config.StateDirName, "_meta", "run.env"))
	return err == nil
}

// WriteProfileJSON stores the resolved profile for a sandbox.
func (b *Base) WriteProfileJSON(slug string, data []byte) error {
	return os.WriteFile(b.ProfileJSONPath(slug), data, 0o644)
}

// SetDomains rewrites the DOMAINS line in run.env.
func (b *Base) SetDomains(domains string) error {
	if err := config.ValidateDomains(strings.Split(domains, ",")); err != nil {
		return err
	}
	runEnv := filepath.Join(b.metaDir(), "run.env")
	env, err := parseEnvFile(runEnv)
	if err != nil {
		return err
	}
	env["DOMAINS"] = domains
	b.Domains = domains
	var sb strings.Builder
	for _, k := range []string{"SRC", "MODEL", "DOMAINS"} {
		fmt.Fprintf(&sb, "%s=%s\n", k, env[k])
	}
	return os.WriteFile(runEnv, []byte(sb.String()), 0o644)
}

// MakeSandbox creates the sandbox directory and pulls its srcs (if the profile
// has any). Nothing is copied unless it is listed in srcs. Progress from the
// srcs pull is written to w.
func (b *Base) MakeSandbox(slug string, w io.Writer) error {
	dest := b.SandboxDir(slug)
	// The workspace dir is created even for a deps-less sandbox, so agents
	// always have the same predictable place to work in.
	if err := os.MkdirAll(filepath.Join(dest, srcs.WorkspaceDir), 0o755); err != nil {
		return err
	}
	if err := b.EnsureHome(slug); err != nil {
		return err
	}
	if err := b.PullDeps(slug, w); err != nil {
		return err
	}
	return b.AppendAgent(slug)
}

// PullDeps vendors the sandbox's deps (host-side) from its stored profile.json
// into the sandbox directory, refreshing the manifest. It is a no-op when the
// sandbox has no stored profile. Callers run it whenever the profile changed so
// newly-listed deps land before the sandbox is entered. Progress is written to w.
func (b *Base) PullDeps(slug string, w io.Writer) error {
	pj := b.ProfileJSONPath(slug)
	if !fileExists(pj) {
		return nil
	}
	opts := srcs.PullOpts{
		ProfileFile:  pj,
		SandboxDir:   b.SandboxDir(slug),
		ManifestFile: b.ManifestPath(slug),
		ProjectRoot:  b.Src,
	}
	if err := srcs.CopyIn(w, opts); err != nil {
		return fmt.Errorf("srcs pull: %w", err)
	}
	return nil
}

// Agents lists the registered sandbox slugs (one per line in agents.list).
func (b *Base) Agents() []string {
	data, err := os.ReadFile(b.AgentsListPath())
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// AppendAgent registers slug if not already present.
func (b *Base) AppendAgent(slug string) error {
	for _, a := range b.Agents() {
		if a == slug {
			return nil
		}
	}
	f, err := os.OpenFile(b.AgentsListPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, slug)
	return err
}

// RemoveAgent drops slug from agents.list.
func (b *Base) RemoveAgent(slug string) error {
	agents := b.Agents()
	var kept []string
	for _, a := range agents {
		if a != slug {
			kept = append(kept, a)
		}
	}
	out := strings.Join(kept, "\n")
	if out != "" {
		out += "\n"
	}
	return os.WriteFile(b.AgentsListPath(), []byte(out), 0o644)
}

// Current returns the active sandbox slug, or "".
func (b *Base) Current() string {
	data, err := os.ReadFile(b.currentPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// SetCurrent sets the active sandbox.
func (b *Base) SetCurrent(slug string) error {
	return os.WriteFile(b.currentPath(), []byte(slug+"\n"), 0o644)
}

// ClearCurrent unsets the active sandbox.
func (b *Base) ClearCurrent() error {
	err := os.Remove(b.currentPath())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// RemoveState deletes a sandbox's on-disk working state: the sandbox dir, the
// metadata/manifest/setup-stamp files and the rotating logs. keepHome preserves
// the private agent home (_home/<slug> — login tokens, shell history), so a
// recreated sandbox needs no re-login; the registration (agents.list, the
// active-sandbox marker) is left to the caller.
func (b *Base) RemoveState(slug string, keepHome bool) {
	paths := []string{
		b.SandboxDir(slug),
		b.MetaFilePath(slug),
		b.ProfileJSONPath(slug),
		b.ManifestPath(slug),
		b.setupStampPath(slug),
	}
	if !keepHome {
		paths = append(paths, b.HomeDir(slug))
	}
	for _, p := range paths {
		_ = os.RemoveAll(p)
	}
	// Remove rotating logs (<slug>.json, <slug>.err, …).
	if entries, err := os.ReadDir(b.logsDir()); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), slug+".") {
				_ = os.Remove(filepath.Join(b.logsDir(), e.Name()))
			}
		}
	}
}

// Remove deletes all state for a single sandbox, including its registration.
func (b *Base) Remove(slug string) error {
	b.RemoveState(slug, false)
	if err := b.RemoveAgent(slug); err != nil {
		return err
	}
	if b.Current() == slug {
		return b.ClearCurrent()
	}
	return nil
}

// gitignoreAllowlist is the .sandboxer/.gitignore body: ignore everything, then
// un-ignore only the three committed sandboxer-owned top-level files. The
// generated state (_meta/, _home/, _logs/, <slug>/) stays ignored by the leading
// "*" — un-ignoring a directory's contents would require re-listing the dir, so
// the allowlist deliberately names only files, keeping the credential-leak guard
// intact.
const gitignoreAllowlist = "*\n!.gitignore\n!" + config.ConfigFileName + "\n!image.nix\n"

// legacyBlanketGitignore is the pre-consolidation body ("ignore everything"),
// which would keep a now-committed config.yaml/image.nix invisible to git.
const legacyBlanketGitignore = "*\n"

// ensureGitignore writes the allowlisting .gitignore, idempotently: it creates
// it when missing and upgrades an existing blanket "*\n" (so an old sandbox's
// committed config stops being ignored). A user-customized gitignore (neither
// the allowlist nor the legacy blanket) is left untouched.
func ensureGitignore(path string) error {
	cur, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		// create below
	case err != nil:
		return err
	case string(cur) == gitignoreAllowlist:
		return nil // already current
	case string(cur) == legacyBlanketGitignore:
		// upgrade the blanket guard to the allowlist
	default:
		return nil // user-customized; leave it alone
	}
	return os.WriteFile(path, []byte(gitignoreAllowlist), 0o644)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func parseEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	env := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '='); i >= 0 {
			env[line[:i]] = line[i+1:]
		}
	}
	return env, sc.Err()
}
