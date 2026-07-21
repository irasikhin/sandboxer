// Package sandbox owns the on-disk runtime state for a project: the base
// metadata (run.env), per-sandbox directories and logs. Internal state lives
// under config.StateDir (an XDG state dir outside the repo), kept separate
// from the committed config (sandboxer.nix) so runtime data — including agent
// homes that may hold login tokens — never lands in git. The worktrees
// themselves live in the project's ./sandboxes (see SandboxesRoot;
// auto-git-ignored, relocatable via the profile's worktreesDir), so the user
// finds them without digging through XDG paths.
//
// A sandbox is a set of SOURCES (see srcs.go): per-repo git worktrees under
// <slug>/, each named by its (explicitly configured) branch. The host worktree
// is always a COMPLETE checkout; a source's `include` narrows what the CONTAINER
// sees by mounting only the listed directories (see Mounts) — with no include,
// <slug>/ is mounted whole. Git metadata is NEVER mounted either way, so the
// mount set is the access boundary and commits happen on the host. A source
// that is not a git repository (or has no commit) is rejected — there is no
// copy-mode fallback.
package sandbox

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/worktree"
)

// Base is a resolved project root and its runtime state directory. It carries
// no git facts: each source resolves its own repository at sync time (see
// srcs.go), and the container never sees git at all.
type Base struct {
	Src     string // absolute project root
	Dir     string // config.StateDir(Src) — the XDG state dir, outside the repo
	Domains string
}

// ResolveBase resolves src to an absolute path, ensures the state dirs exist,
// seeds run.env on first use, and loads it. The runtime state lives under
// config.StateDir (outside the repo); the committed config stays at
// <Src>/sandboxer.yaml and is handled separately by the config package.
func ResolveBase(src string) (*Base, error) {
	abs, err := filepath.Abs(src)
	if err != nil {
		return nil, fmt.Errorf("no such directory: %s", src)
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("no such directory: %s", src)
	}
	dir := config.StateDir(abs)
	if dir == "" {
		return nil, fmt.Errorf("cannot determine state directory: set $XDG_STATE_HOME or $SANDBOXER_STATE (no home directory found)")
	}
	b := &Base{Src: abs, Dir: dir}
	if err := os.MkdirAll(b.metaDir(), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(b.logsDir(), 0o755); err != nil {
		return nil, err
	}
	runEnv := filepath.Join(b.metaDir(), "run.env")
	if _, err := os.Stat(runEnv); err != nil {
		d := config.LoadDefaults()
		content := fmt.Sprintf("SRC=%s\nDOMAINS=%s\n", abs, d.Domains)
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
	return b, nil
}

// OpenBase locates an existing project base read-only: it neither creates the
// state dirs nor seeds run.env, so it is safe to call on a `cd` (e.g. the direnv
// hook) without any side effects. It returns (nil, nil) when src has no
// initialized sandboxer state — the caller treats that as "not a sandboxer
// project". Any run.env that is present is loaded for Domains.
func OpenBase(src string) (*Base, error) {
	abs, err := filepath.Abs(src)
	if err != nil {
		return nil, err
	}
	// RunEnvExists already resolved (and found) the state dir, so config.StateDir
	// is guaranteed non-empty here.
	if !RunEnvExists(abs) {
		return nil, nil
	}
	b := &Base{Src: abs, Dir: config.StateDir(abs)}
	if env, err := parseEnvFile(filepath.Join(b.metaDir(), "run.env")); err == nil {
		b.Domains = env["DOMAINS"]
	}
	return b, nil
}

func (b *Base) metaDir() string  { return filepath.Join(b.Dir, "_meta") }
func (b *Base) logsDir() string  { return filepath.Join(b.Dir, "_logs") }
func (b *Base) homeRoot() string { return filepath.Join(b.Dir, "_home") }

// SandboxesRoot is the DEFAULT location for a project's sandbox worktrees:
// ./sandboxes inside the project, right next to sandboxer.nix. The dir is
// auto-added to the project's .gitignore (see ensureIgnored) so working
// copies can never land in a commit. A profile's worktreesDir overrides it
// per sandbox (see worktreesRoot).
func SandboxesRoot(src string) string {
	return filepath.Join(src, "sandboxes")
}

// legacySiblingRoot is the pre-v0.45 default worktree location
// (../<project>-sandboxes, beside the project) — sandboxer-owned by naming,
// still swept by clean so upgrades leave no orphans behind.
func legacySiblingRoot(src string) string {
	return filepath.Join(filepath.Dir(src), filepath.Base(src)+"-sandboxes")
}

// worktreesRoot resolves where slug's worktrees live: the stored profile's
// worktreesDir (absolute, ~-prefixed, or relative to the project root), or
// the default in-project SandboxesRoot. Reading the STORED snapshot keeps
// every command's view of the sandbox location consistent between syncs.
func (b *Base) worktreesRoot(slug string) string {
	dir := b.profileWorktreesDir(slug)
	if dir == "" {
		return SandboxesRoot(b.Src)
	}
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(dir, "~"), "/"))
		}
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(b.Src, dir)
	}
	return filepath.Clean(dir)
}

// SandboxDir is the directory for a sandbox: <worktreesRoot>/<slug>. The
// managed worktrees inside are grouped by repo and named by their branch
// (see srcs.go), so the on-disk path spells everything out:
// sandboxes/<slug>/<repo>/<branch>.
func (b *Base) SandboxDir(slug string) string {
	return filepath.Join(b.worktreesRoot(slug), slug)
}

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

// Gen returns the sandbox directory's generation: a counter bumped by SyncSrcs
// every time the dir has to be created from nothing ("" until the first bump —
// a sandbox created by an older version and never recreated). The CLI passes
// it to the backend (RunOpts.DestGen), where it becomes a container env var
// and thereby part of the session ConfigHash — so a hand-deleted-and-recreated
// sandbox tree invalidates the session container that still bind-mounts the
// deleted directory.
func (b *Base) Gen(slug string) string {
	data, err := os.ReadFile(b.genPath(slug))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// bumpGen advances the sandbox directory's generation (absent reads as 0).
func (b *Base) bumpGen(slug string) error {
	n, _ := strconv.Atoi(b.Gen(slug)) // "" or garbage reads as 0
	return os.WriteFile(b.genPath(slug), []byte(strconv.Itoa(n+1)+"\n"), 0o644)
}

func (b *Base) genPath(slug string) string {
	return filepath.Join(b.metaDir(), slug+".gen")
}

// ProfileJSONPath, MetaFilePath locate per-sandbox metadata files.
func (b *Base) ProfileJSONPath(slug string) string {
	return filepath.Join(b.metaDir(), slug+".profile.json")
}

// SessionStatePath is the host file holding the sandbox's saved tmux layout
// (backend.TmuxSession JSON), captured before a container is replaced and
// replayed on the next attach. Under _meta like the other per-sandbox state;
// deleted only by a full RemoveState (rm/clean/recreate --full), never by a
// routine recreate — the session is the user's, discarded only on explicit rm.
func (b *Base) SessionStatePath(slug string) string {
	return filepath.Join(b.metaDir(), slug+".session.json")
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
	dir := config.StateDir(abs)
	if dir == "" {
		return false
	}
	_, err = os.Stat(filepath.Join(dir, "_meta", "run.env"))
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
	for _, k := range []string{"SRC", "DOMAINS"} {
		fmt.Fprintf(&sb, "%s=%s\n", k, env[k])
	}
	return os.WriteFile(runEnv, []byte(sb.String()), 0o644)
}

// MakeSandbox creates a sandbox's working tree: the <slug>/ dir populated with
// one git worktree per configured source (see SyncSrcs; srcs must be listed
// explicitly, each naming its branch — the scaffolded config seeds both).
// Progress is written to w. A source that is not a git repository (or has no
// commit) is rejected — sandboxer is git-only.
func (b *Base) MakeSandbox(slug string, w io.Writer) error {
	if err := b.EnsureHome(slug); err != nil {
		return err
	}
	if _, err := b.SyncSrcs(slug, w); err != nil {
		return err
	}
	return b.AppendAgent(slug)
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

// RemoveState deletes a sandbox's on-disk working state: the managed source
// worktrees under <slug>/, the metadata/manifest/setup-stamp files and the
// rotating logs. keepHome preserves the private agent home (_home/<slug> —
// login tokens, shell history), so a recreated sandbox needs no re-login; the
// registration (agents.list, the active-sandbox marker) is left to the caller.
// Managed worktrees are unregistered from their repos (admin entries pruned);
// every source branch is kept so commits survive (RemoveSandboxBranches is the
// full reset). Adopted worktrees are never touched — they were only mounted.
func (b *Base) RemoveState(slug string, keepHome bool) {
	dest := b.SandboxDir(slug)
	// Defense-in-depth: dest must be strictly UNDER the worktrees root. A valid
	// slug always is (dest = <root>/<slug>); an empty, "." or ".." slug would
	// resolve dest onto the root itself or — via filepath.Join(root, "..") — the
	// project root, and the os.RemoveAll below would then wipe it. The CLI
	// rejects such a slug up front (config.ValidSlug), so reaching here means a
	// mis-called API: refuse rather than delete outside the sandbox tree.
	if root := b.worktreesRoot(slug); dest == root ||
		!strings.HasPrefix(dest, root+string(filepath.Separator)) {
		return
	}
	for _, s := range b.Srcs(slug) {
		if s.Managed && worktree.IsWorktree(s.Path) {
			_ = worktree.Remove(s.RepoRoot, s.Path)
		}
	}
	// Pre-srcs-model sandboxes: the sandbox dir itself was a worktree of the
	// project repo.
	if worktree.IsWorktree(dest) {
		if top, _, ok := worktree.Detect(b.Src); ok {
			_ = worktree.Remove(top, dest)
		}
	}
	paths := []string{
		dest,
		b.MetaFilePath(slug),
		b.ProfileJSONPath(slug),
		b.SrcsMetaPath(slug),
		b.setupStampPath(slug),
		b.genPath(slug),
	}
	if !keepHome {
		// A full removal (rm/clean/recreate --full) discards the saved session
		// layout too; a routine recreate (keepHome) keeps it so the next attach
		// restores it — the session dies only on an explicit rm.
		paths = append(paths, b.HomeDir(slug), b.SessionStatePath(slug))
	}
	for _, p := range paths {
		_ = os.RemoveAll(p)
	}
	// Tidy the sandbox's worktrees root when this was its last occupant
	// (os.Remove refuses a non-empty dir, which is exactly the wanted
	// behavior — and keeps a user-chosen worktreesDir like "." safe).
	_ = os.Remove(b.worktreesRoot(slug))
	// Remove rotating logs (<slug>.json, <slug>.err, …).
	if entries, err := os.ReadDir(b.logsDir()); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), slug+".") {
				_ = os.Remove(filepath.Join(b.logsDir(), e.Name()))
			}
		}
	}
}

// CleanWorktrees removes every sandbox worktree for the project. No root is
// ever removed wholesale — the default ./sandboxes lives inside the project
// and a worktreesDir may point anywhere — so only the per-sandbox dirs and
// _detached/ under each root go (the root itself is tidied when empty); the
// one exception is the sandboxer-owned legacy sibling ../<project>-sandboxes.
// Must run BEFORE the state dir is wiped — the per-sandbox roots are resolved
// from the stored profile snapshots. Returns the paths removed.
func (b *Base) CleanWorktrees() []string {
	var removed []string
	rm := func(p string) {
		if _, err := os.Stat(p); err != nil {
			return
		}
		if os.RemoveAll(p) == nil {
			removed = append(removed, p)
		}
	}
	roots := map[string]bool{SandboxesRoot(b.Src): true}
	for _, slug := range b.Agents() {
		root := b.worktreesRoot(slug)
		roots[root] = true
		rm(filepath.Join(root, slug))
	}
	for root := range roots {
		rm(filepath.Join(root, "_detached"))
		_ = os.Remove(root) // empty-only tidy; never wholesale on a shared dir
	}
	rm(legacySiblingRoot(b.Src))
	return removed
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

// RemoveSandboxBranches force-deletes the git branches sandboxer itself
// MINTED for the sandbox (the branch did not exist at first sync; recorded
// per source). Normal teardown keeps branches so work survives; a full reset
// (recreate --full) calls this for a clean slate. A branch that existed
// before the sandbox is never deleted — it is the user's, not sandboxer's.
// srcs is the source list captured BEFORE the teardown (RemoveState deletes
// the recorded meta, and git refuses to delete a branch still checked out in
// a worktree).
func (b *Base) RemoveSandboxBranches(slug string, srcs []Source) {
	for _, s := range srcs {
		if s.AutoBranch {
			_ = worktree.DeleteBranch(s.RepoRoot, s.Branch)
		}
	}
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
