package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/worktree"
)

// Source is one resolved sandbox source: a repository worktree the container
// gets access to. The resolved list is recorded at _meta/<slug>.srcs.json at
// every sync, so teardown, recreate --full, clean and the mount assembly know
// what a sandbox spans even after the config changed.
type Source struct {
	RepoRoot string   `json:"repoRoot"`          // abs toplevel of the source repository
	Path     string   `json:"path"`              // abs worktree dir exposed to the container
	Branch   string   `json:"branch"`            // branch the worktree is on
	Include  []string `json:"include,omitempty"` // narrowing directory paths/patterns (see config.ValidateInclude)
	// Managed marks a worktree sandboxer created under <slug>/ and may tear
	// down. An adopted worktree (a srcs entry whose branch: was already checked
	// out in a worktree of the user's) is never touched by teardown — it is
	// mounted, and reached from the sandbox through Link.
	Managed bool `json:"managed"`
	// AutoBranch marks a branch sandboxer MINTED itself (it did not exist at
	// first sync); recreate --full deletes only these — a branch that existed
	// before the sandbox is never deleted.
	AutoBranch bool `json:"autoBranch,omitempty"`
	// Remote is the git URL this source was cloned from, when the srcs entry
	// named a URL rather than a local path. Empty for a local source. RepoRoot
	// then points at the host-side cache clone under _remotes/ (kept across
	// teardown, wiped by clean).
	Remote string `json:"remote,omitempty"`
	// Link is the slot an ADOPTED source occupies inside the sandbox directory
	// (<slug>/<branch>/<repo>): a symlink to Path, the checkout git holds
	// elsewhere. Empty for a managed source, whose Path already IS that slot.
	// See linkAdoptedSrc for why being mounted is not enough.
	Link string `json:"link,omitempty"`
}

// Name is the label this source is addressed by within its sandbox: the last
// path element of its worktree — the repo-level leaf for a managed source
// (<slug>/<branch>/<NAME>, deduped by assignSandboxPaths when two repos share
// a basename), the checkout's own dir name for an adopted one. Unique across
// a sandbox's managed sources. `path <slug> <name>` selects one source by it.
func (s Source) Name() string {
	return filepath.Base(s.Path)
}

// SrcsMetaPath locates the recorded source list for a sandbox.
func (b *Base) SrcsMetaPath(slug string) string {
	return filepath.Join(b.metaDir(), slug+".srcs.json")
}

// Srcs reads the recorded source list for a sandbox (nil when none recorded —
// the sandbox predates the srcs model or was never synced).
func (b *Base) Srcs(slug string) []Source {
	data, err := os.ReadFile(b.SrcsMetaPath(slug))
	if err != nil {
		return nil
	}
	var out []Source
	if json.Unmarshal(data, &out) != nil {
		return nil
	}
	return out
}

// SourceNames lists the sources' names in srcs order (see Source.Name), for
// diagnostics that must spell out the choices.
func SourceNames(srcs []Source) []string {
	names := make([]string, len(srcs))
	for i, s := range srcs {
		names[i] = s.Name()
	}
	return names
}

// FindSource returns the single source addressed by name — its Name(), or the
// repo basename as a convenience when that is unambiguous. It errors, listing
// the available names, when nothing matches or the name is ambiguous, so a
// caller can surface a usable diagnostic instead of guessing.
func FindSource(srcs []Source, name string) (Source, error) {
	var hits []Source
	for _, s := range srcs {
		if s.Name() == name || filepath.Base(s.RepoRoot) == name {
			hits = append(hits, s)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return Source{}, fmt.Errorf("sandbox has no source %q — its sources are: %s",
			name, strings.Join(SourceNames(srcs), ", "))
	default:
		return Source{}, fmt.Errorf("source %q is ambiguous — name one exactly: %s",
			name, strings.Join(SourceNames(srcs), ", "))
	}
}

func (b *Base) writeSrcs(slug string, srcs []Source) error {
	data, err := json.MarshalIndent(srcs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(b.SrcsMetaPath(slug), append(data, '\n'), 0o644)
}

// detachedDir holds worktrees of sources that were dropped from a sandbox's
// srcs — moved aside (uncommitted work intact) instead of destroyed, and out
// of the mounted <slug>/ tree so the container loses access. It lives under
// the sandbox's worktrees root (same filesystem as the worktrees, so setting
// one aside is always a rename); `clean` wipes it with the rest.
func (b *Base) detachedDir(slug string) string {
	return filepath.Join(b.worktreesRoot(slug), "_detached")
}

// remotesDir holds host-side clones of remote sources (srcs whose src is a git
// URL). Each URL is cloned once into <name>-<hash>/ and reused as an ordinary
// local repo — sandbox worktrees are cut off it exactly as for a local src.
// Kept across a sandbox teardown (shared, like a local repo); `clean` wipes it
// with the rest of the state dir.
func (b *Base) remotesDir() string { return filepath.Join(b.Dir, "_remotes") }

// ensureRemoteCache returns the host-side cache clone for a remote src URL,
// cloning it on first use (clone-once: a present clone is reused as-is, so
// enter/exec stay fast and offline — recreate is what re-fetches, see
// RefreshRemotes). name is the derived repo basename for the worktree dir.
func (b *Base) ensureRemoteCache(url string, w io.Writer) (cacheDir, name string, err error) {
	name = worktree.RepoName(url)
	cacheDir = filepath.Join(b.remotesDir(), name+"-"+shortPathHash(url))
	if _, _, ok := worktree.Detect(cacheDir); ok {
		return cacheDir, name, nil
	}
	if err := os.MkdirAll(b.remotesDir(), 0o755); err != nil {
		return "", "", err
	}
	_ = os.RemoveAll(cacheDir) // clear any half-finished earlier clone
	if err := worktree.Clone(url, cacheDir, w); err != nil {
		_ = os.RemoveAll(cacheDir)
		return "", "", err
	}
	return cacheDir, name, nil
}

// RefreshRemotes fetches every remote source's cache clone for slug (recreate's
// clone-once refresh point). Best-effort per source so an offline or moved
// remote does not block rebuilding the rest; the first hard error is returned
// after attempting all. Sources are read from the stored profile.
func (b *Base) RefreshRemotes(slug string, w io.Writer) error {
	var firstErr error
	seen := map[string]bool{}
	for _, spec := range b.profileSrcs(slug) {
		if !worktree.IsRemoteURL(spec.Src) || seen[spec.Src] {
			continue
		}
		seen[spec.Src] = true
		cacheDir := filepath.Join(b.remotesDir(), worktree.RepoName(spec.Src)+"-"+shortPathHash(spec.Src))
		if _, _, ok := worktree.Detect(cacheDir); !ok {
			continue // not cloned yet — resolveSrcs will clone it fresh
		}
		if err := worktree.FetchCache(cacheDir, w); err != nil {
			if w != nil {
				fmt.Fprintf(w, "sandboxer: refresh %s failed: %v (keeping the cached copy)\n", worktree.RepoName(spec.Src), err)
			}
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// profileSrcs reads the srcs list from the sandbox's stored profile.json.
// There is no implicit default — an absent profile or empty list is rejected
// by resolveSrcs (the scaffolded config seeds an explicit src + branch).
func (b *Base) profileSrcs(slug string) []config.Src {
	data, err := os.ReadFile(b.ProfileJSONPath(slug))
	if err != nil {
		return nil
	}
	var p struct {
		Srcs []config.Src `json:"srcs"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return nil
	}
	return p.Srcs
}

// profileWorktreesDir reads the worktreesDir override from the sandbox's
// stored profile.json ("" = the default sibling root).
func (b *Base) profileWorktreesDir(slug string) string {
	data, err := os.ReadFile(b.ProfileJSONPath(slug))
	if err != nil {
		return ""
	}
	var p struct {
		WorktreesDir string `json:"worktreesDir"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return ""
	}
	return p.WorktreesDir
}

// resolveSrcs maps the configured srcs entries onto resolved Sources for slug:
// paths are validated (must exist, be a repo toplevel with at least one
// commit, no repo twice), every entry names its branch EXPLICITLY (an entry
// without branch: is an error — there is no default naming), and managed
// worktrees are placed under <slug>/ grouped by their branch (see
// assignManagedPaths). Entry order is preserved. An empty list is an error,
// never an implicit "current directory": what a sandbox exposes is always
// spelled out in the config.
//
// prev is the recorded source list from earlier syncs: it carries each
// branch's minted-vs-reused verdict forward and names the recorded branch in
// the missing-branch error hint.
func (b *Base) resolveSrcs(slug string, specs []config.Src, prev []Source, w io.Writer) ([]Source, error) {
	if len(specs) == 0 {
		return nil, fmt.Errorf("srcs is empty — a sandbox needs at least one source; add to %s, e.g.:\n"+
			"  srcs = [\n"+
			"    { src = \".\"; branch = \"devops/my-change\"; }   # this repo\n"+
			"    { src = \"../other-repo\"; branch = \"devops/my-change\"; }\n"+
			"  ];\n"+
			"(edit it with: sandboxer config edit)", config.ConfigFileName)
	}
	// Validate the patterns before touching git: `create` resolves the runtime
	// (where config.ValidateSrcs also runs) only AFTER materializing the
	// sandbox, so this is what stops a bad include from getting that far.
	if err := config.ValidateSrcs(specs); err != nil {
		return nil, err
	}
	recorded := map[string]Source{} // repo toplevel -> the source as last synced
	for _, p := range prev {
		if p.Branch != "" {
			recorded[p.RepoRoot] = p
		}
	}
	seenRepo := map[string]bool{}
	out := make([]Source, 0, len(specs))
	for _, spec := range specs {
		path := spec.Src
		if path == "" {
			return nil, errors.New("srcs entry with an empty src — every entry needs src: <path-to-repo>")
		}
		if worktree.IsRemoteURL(spec.Src) {
			// Remote source: clone into the host-side cache (once), then treat the
			// cache exactly like a local repo — a managed worktree under <slug>/.
			cacheDir, _, err := b.ensureRemoteCache(spec.Src, w)
			if err != nil {
				return nil, fmt.Errorf("srcs entry %q: %w", spec.Src, err)
			}
			if seenRepo[cacheDir] {
				return nil, fmt.Errorf("srcs entry %q: remote %s listed twice", spec.Src, spec.Src)
			}
			seenRepo[cacheDir] = true

			// From here the cache clone IS the repo: branch is required and the
			// worktree path is assigned by assignManagedPaths, exactly as for a
			// local source. A remote can never be "already checked out
			// elsewhere" — the cache is ours — so there is no adopt branch.
			if spec.Branch == "" {
				return nil, fmt.Errorf("srcs entry %q: branch is required — every source names its "+
					"branch explicitly, e.g. { src = %q; branch = \"main\"; }", spec.Src, spec.Src)
			}
			// Check out origin/<branch> when the remote has it, so branch: names
			// an UPSTREAM branch instead of forking a new one off the default.
			worktree.PrepareBranch(cacheDir, spec.Branch)
			src := Source{RepoRoot: cacheDir, Include: spec.Include, Remote: spec.Src,
				Branch: spec.Branch, Managed: true}
			if err := worktree.ValidBranch(cacheDir, src.Branch); err != nil {
				return nil, fmt.Errorf("srcs entry %q: %w", spec.Src, err)
			}
			if p, ok := recorded[cacheDir]; ok && p.Branch == src.Branch {
				src.AutoBranch = p.AutoBranch
			} else {
				src.AutoBranch = !worktree.BranchExists(cacheDir, src.Branch)
			}
			out = append(out, src)
			continue
		}
		if !filepath.IsAbs(path) {
			// Relative srcs paths resolve against the PROJECT ROOT — one rule
			// regardless of where the profile file lives (root or -f).
			path = filepath.Join(b.Src, path)
		}
		path = filepath.Clean(path)
		if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
			return nil, fmt.Errorf("srcs entry %q: no such directory", spec.Src)
		}
		top, _, ok := worktree.Detect(path)
		if !ok {
			return nil, fmt.Errorf("srcs entry %q: not a git repository with a commit — "+
				"point src at a repo (sandboxer config edit), or git init && git commit it "+
				"(non-git trees come in via extraMounts)", spec.Src)
		}
		if path != top {
			return nil, fmt.Errorf("srcs entry %q: must point at the repository root (%s)", spec.Src, top)
		}
		if seenRepo[top] {
			return nil, fmt.Errorf("srcs entry %q: repository %s listed twice", spec.Src, top)
		}
		seenRepo[top] = true

		src := Source{RepoRoot: top, Include: spec.Include}
		src.Branch = spec.Branch
		if src.Branch == "" {
			hint := ""
			if p, ok := recorded[top]; ok {
				hint = fmt.Sprintf(" (this sandbox's recorded worktree is on %q)", p.Branch)
			}
			return nil, fmt.Errorf("srcs entry %q: branch is required — every source names its "+
				"branch explicitly, e.g. { src = %q; branch = \"devops/my-change\"; }%s",
				spec.Src, spec.Src, hint)
		}
		if err := worktree.ValidBranch(top, src.Branch); err != nil {
			return nil, fmt.Errorf("srcs entry %q: %w", spec.Src, err)
		}
		if p, ok := recorded[top]; ok && p.Branch == src.Branch {
			// The minted-vs-reused verdict was decided at first sync and
			// travels with the branch (it exists either way afterwards).
			src.AutoBranch = p.AutoBranch
		} else {
			// Only a branch sandboxer mints itself may be deleted by a full
			// reset; a branch that already existed is reused, not owned.
			src.AutoBranch = !worktree.BranchExists(top, src.Branch)
		}
		if wt, ok := worktree.FindWorktree(top, src.Branch); ok &&
			!strings.HasPrefix(wt, b.SandboxDir(slug)+string(filepath.Separator)) &&
			!isSetAside(wt) && dirExists(wt) {
			// The branch is already checked out somewhere OUTSIDE this sandbox:
			// adopt that checkout — git allows only one worktree per branch, so
			// there is no second tree to make. This sandbox's own managed
			// worktree is not "somewhere": it stays managed, or a re-sync would
			// adopt it. Nor is a checkout set aside under _detached/ or one whose
			// directory is gone (a stale registration): those stay managed too —
			// materializeSrc re-attaches the one and prunes the other.
			//
			// Not every checkout may be adopted, though: checkAdoptable refuses
			// the two that would hand the sandbox a tree it has no business
			// holding. include is kept and honored — an adopted source narrows
			// exactly like a managed one, since narrowing is a mount-set concern
			// and says nothing about the working tree.
			if err := checkAdoptable(spec.Src, src.Branch, wt); err != nil {
				return nil, err
			}
			src.Path = wt
			src.AutoBranch = false
			out = append(out, src)
			continue
		}
		src.Managed = true
		out = append(out, src)
	}
	if err := assignSandboxPaths(b.SandboxDir(slug), out); err != nil {
		return nil, err
	}
	return out, nil
}

// checkAdoptable decides whether an existing checkout of the branch may be
// adopted as a sandbox source. Both refusals are hard errors rather than
// warnings: a sandbox that silently exposes the wrong tree is precisely the
// failure this guards, and either case has a one-line fix the message names.
//
//   - The repository's OWN working checkout (its .git is a real DIRECTORY, not
//     a linked worktree's pointer file). Mounting it gives the agent the tree
//     the user works in — and carries .git along with it, so git works inside
//     the sandbox and its hooks, config and filters become host-side code the
//     agent can rewrite. That is the whole reason git never enters the
//     container (see SECURITY.md); adoption must not be the way around it.
//   - A checkout that belongs to a SANDBOX, this project's or another's.
//     Adopting it makes two sandboxes share one working tree — the opposite of
//     what a sandbox is — and leaves the adopting one's own directory empty.
func checkAdoptable(spec, branch, wt string) error {
	if hasGitDir(wt) {
		return fmt.Errorf("srcs entry %q: branch %q is checked out in the repository itself (%s) — "+
			"sandboxer will not mount your working checkout into a sandbox: the agent would edit the "+
			"tree you work in, and the mount would carry its .git along. Give this source its own "+
			"branch (e.g. branch = %q), or switch that checkout off %s first",
			spec, branch, wt, branch+"-sb", branch)
	}
	if slug, dir, ok := sandboxOwning(wt); ok {
		return fmt.Errorf("srcs entry %q: branch %q is already checked out by sandbox %q (%s) — "+
			"two sandboxes cannot share one working tree; give this source its own branch",
			spec, branch, slug, dir)
	}
	return nil
}

// hasGitDir reports whether path is a repository's own working checkout: its
// .git is a real directory, so bind-mounting the tree carries the git directory
// into the container. A LINKED worktree's .git is a pointer file naming a host
// path that is never mounted, which is why those stay safe to adopt.
func hasGitDir(path string) bool {
	fi, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil && fi.IsDir()
}

// sandboxOwning reports the sandbox whose directory contains path. The lookup is
// HOST-WIDE (Projects indexes every project sandboxer holds state for), so a
// checkout belonging to a sandbox of a DIFFERENT project is recognized too — the
// same branch name in two projects' configs is exactly how that happens.
// Best-effort by construction: a sandbox whose project has no state dir cannot
// be seen, and the caller reads "no owner" as adoptable.
func sandboxOwning(path string) (slug, dir string, ok bool) {
	sep := string(filepath.Separator)
	for _, p := range Projects() {
		for _, s := range p.Agents() {
			if d := p.SandboxDir(s); strings.HasPrefix(path, d+sep) {
				return s, d, true
			}
		}
	}
	return "", "", false
}

// assignSandboxPaths gives EVERY source its slot under root, grouped by branch
// with the repo as the leaf — <root>/<branch>/<repo>, branch slashes becoming
// directories — so one branch spanning several repos reads as a unit
// (…/mybox/feat/BDP-5291/miko-java). Repos sharing a basename are deduped with
// a short path hash, which is also what keeps Source.Name unique.
//
// A managed source's slot is its worktree (Path). An ADOPTED source's is a
// symlink to the checkout git holds elsewhere (Link, made by linkAdoptedSrc):
// both kinds occupy the sandbox directory, because that directory is the
// container's workdir and a source reachable only from somewhere else on the
// host is a source the user listed and cannot find. Sharing one namespace is
// therefore not cosmetic — two sources must never be handed the same slot.
//
// Branch dirs of DIFFERENT repos share the namespace under root, so a repo leaf
// can collide with another source's branch path (repo "x" on branch "feat" vs
// any repo on branch "feat/x" — git's ref rules forbid that only within one
// repo); the pairwise check below refuses a layout that would nest one source
// inside another. For a link that check is load-bearing beyond tidiness: a
// worktree assigned UNDER a symlink would be created through it, inside the
// user's own tree.
func assignSandboxPaths(root string, srcs []Source) error {
	seenName := map[string]bool{}
	sep := string(filepath.Separator)
	slots := make([]string, len(srcs))
	for i := range srcs {
		name := filepath.Base(srcs[i].RepoRoot)
		if seenName[name] {
			name = name + "-" + shortPathHash(srcs[i].RepoRoot)
		}
		seenName[name] = true
		p := filepath.Join(root, filepath.FromSlash(srcs[i].Branch), name)
		// ValidBranch already rejects ".."-style names; this is the belt that
		// guarantees no branch-derived path ever leaves the sandbox dir.
		if !strings.HasPrefix(p, root+sep) {
			return fmt.Errorf("srcs branch %q escapes the sandbox directory", srcs[i].Branch)
		}
		if srcs[i].Managed {
			srcs[i].Path = p
		} else {
			srcs[i].Link = p
		}
		slots[i] = p
	}
	for i := range slots {
		for j := range slots {
			if j == i {
				continue
			}
			if strings.HasPrefix(slots[j], slots[i]+sep) {
				return fmt.Errorf("srcs collide: %s (branch %q) would sit inside %s (branch %q) — rename one of the branches",
					slots[j], srcs[j].Branch, slots[i], srcs[i].Branch)
			}
		}
	}
	return nil
}

// shortPathHash disambiguates two source repos that share a base name.
func shortPathHash(p string) string {
	sum := sha256.Sum256([]byte(p))
	return hex.EncodeToString(sum[:])[:4]
}

// SyncSrcs converges the sandbox's on-disk sources onto the stored profile:
// it (re-)resolves srcs (every source names its branch explicitly — see
// resolveSrcs), creates missing managed worktrees, moves a managed worktree
// whose assigned path changed while source and branch did not (a layout
// change across sandboxer versions, a dedup rename — see relocateSrc), widens
// any left narrow by a pre-view-mounts sandboxer (Unsparse), sets aside
// managed worktrees whose source was dropped (or whose branch: changed) under
// _detached/, and records the result in
// _meta/<slug>.srcs.json. It runs on create and on every enter/exec, so
// editing srcs opens access without recreating anything: the container's one
// stable mount is the <slug>/ dir this function populates, and a live session
// sees the change immediately.
func (b *Base) SyncSrcs(slug string, w io.Writer) ([]Source, error) {
	dest := b.SandboxDir(slug)
	if worktree.IsWorktree(dest) {
		return nil, fmt.Errorf("sandbox %q predates the srcs model (its dir is itself a worktree) — "+
			"rebuild it: sandboxer recreate %s", slug, slug)
	}
	prev := b.Srcs(slug)
	srcs, err := b.resolveSrcs(slug, b.profileSrcs(slug), prev, w)
	if err != nil {
		return nil, err
	}
	// A worktreesDir change on an EXISTING sandbox would strand its worktrees
	// under the old root and force a cross-root (possibly cross-filesystem) git
	// worktree move — which fails on EXDEV and wedges the sync. Refuse in place
	// and route to recreate, which rebuilds cleanly at the new location (branches
	// and commits are kept, and recreate itself now guards uncommitted work).
	// Only trips when a materialized managed worktree actually sits outside the
	// current root, so a first sync (empty prev) and an unchanged root pass.
	curRoot := b.worktreesRoot(slug)
	for _, p := range prev {
		if p.Managed && worktree.IsWorktree(p.Path) &&
			!strings.HasPrefix(p.Path, curRoot+string(filepath.Separator)) {
			return nil, fmt.Errorf("sandbox %q already has a worktree at %s, but worktreesDir now resolves to %s — "+
				"relocating an existing sandbox in place is not supported; rebuild it at the new location with "+
				"'sandboxer recreate %s' (branches and commits are kept — commit uncommitted work first, or "+
				"'sandboxer recreate %s --force' to discard it)",
				slug, p.Path, curRoot, slug, slug)
		}
	}
	if !dirExists(dest) {
		// The sandbox dir is being created from nothing — first sync, or the
		// tree was deleted by hand / relocated by worktreesDir. Bump its
		// generation so a session container created against the OLD directory
		// (whose bind mounts now show a deleted tree) reads as stale.
		if err := b.bumpGen(slug); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return nil, err
	}
	if err := b.ensureIgnored(b.worktreesRoot(slug), w); err != nil {
		return nil, err
	}

	// Set aside previously managed worktrees that no longer correspond to a
	// resolved source (dropped from srcs, renamed, or switched branch) — but
	// first relocate one whose source is still wanted and only its managed
	// path changed: a rename keeps uncommitted work and ignored build caches
	// that a detach-and-recheckout would destroy.
	want := map[string]string{}    // managed path -> branch
	newHome := map[string]string{} // repoRoot\x00branch -> managed path
	for _, s := range srcs {
		if s.Managed {
			want[s.Path] = s.Branch
			newHome[s.RepoRoot+"\x00"+s.Branch] = s.Path
		}
	}
	for _, p := range prev {
		if !p.Managed {
			continue
		}
		if br, ok := want[p.Path]; ok {
			if !worktree.IsWorktree(p.Path) {
				continue // not materialized yet; Ensure below will create it
			}
			cur := worktree.CurrentBranch(p.Path)
			if cur == br {
				continue
			}
			if cur == "" {
				// No branch to compare: a detached HEAD (mid-rebase, a pinned
				// commit) or a git failure. "Unknown" is not "switched" —
				// leave the worktree in place rather than set aside work that
				// may well be the wanted branch's.
				if w != nil {
					fmt.Fprintf(w, "sandboxer: srcs %s: worktree is not on a branch (detached HEAD?) — left in place\n",
						filepath.Base(p.RepoRoot))
				}
				continue
			}
		}
		if to, ok := newHome[p.RepoRoot+"\x00"+p.Branch]; ok && to != p.Path && worktree.IsWorktree(p.Path) {
			// Only a worktree still on its recorded branch moves — with detached
			// HEAD counting as "still": "unknown" is not "switched", and leaving
			// the tree at the old path would wedge Ensure on git's
			// one-worktree-per-branch rule. A hand-switched worktree falls
			// through to detachSrc, exactly like a branch: change.
			if cur := worktree.CurrentBranch(p.Path); cur == p.Branch || cur == "" {
				err := b.relocateSrc(slug, p, to, w)
				if err == nil {
					continue
				}
				// The target can legitimately still be occupied mid-migration
				// (crossed repo/branch names, a dedup swap): set the tree aside
				// instead — materializeSrc re-attaches it, work intact.
				if w != nil {
					fmt.Fprintf(w, "sandboxer: srcs %s: move to %s failed (%v) — setting the worktree aside instead\n",
						filepath.Base(p.RepoRoot), to, err)
				}
			}
		}
		if err := b.detachSrc(slug, p, w); err != nil {
			return nil, err
		}
	}

	// Drop adopted-source links the profile no longer names: the source was
	// removed, its branch changed, or it became managed and now wants that slot
	// as a real worktree. Only a symlink is ever removed (see removeLink), so a
	// worktree that took the slot in the meantime is safe.
	wantLink := map[string]bool{}
	for _, s := range srcs {
		if s.Link != "" {
			wantLink[s.Link] = true
		}
	}
	for _, p := range prev {
		if p.Link != "" && !wantLink[p.Link] {
			removeLink(p.Link, b.SandboxDir(slug))
		}
	}

	for _, s := range srcs {
		if s.Managed {
			if worktree.IsWorktree(s.Path) {
				// Widen a tree left narrow by a pre-view-mounts sandboxer.
				if _, err := worktree.Unsparse(s.Path, w); err != nil {
					return nil, err
				}
			} else if err := b.materializeSrc(s, w); err != nil {
				return nil, err
			}
		} else if err := b.linkAdoptedSrc(s, w); err != nil {
			return nil, err
		}
		if err := checkViewDirs(s); err != nil {
			return nil, err
		}
	}
	warnAdoptedUnreachable(srcs, w)
	return srcs, b.writeSrcs(slug, srcs)
}

// linkAdoptedSrc surfaces an adopted source inside the sandbox directory: a
// symlink at s.Link pointing at the checkout git holds elsewhere. Being mounted
// is not enough — an adopted tree is bind-mounted at its OWN host path, which is
// nowhere near the sandbox directory the container starts in, so without the
// link a source the user listed simply cannot be found from inside. The link
// resolves in the container precisely because that host path IS mounted, at the
// same path (see Mounts).
//
// A slot occupied by something that is not a symlink is an error, never a
// clobber: it is real content (a worktree the detach pass could not remove, a
// hand-made directory) and losing it silently is worse than failing here.
func (b *Base) linkAdoptedSrc(s Source, w io.Writer) error {
	if s.Link == "" {
		return nil
	}
	switch cur, err := os.Readlink(s.Link); {
	case err == nil && cur == s.Path:
		return nil // already pointing where it should
	case err == nil:
		if err := os.Remove(s.Link); err != nil {
			return err
		}
	default:
		if _, err := os.Lstat(s.Link); err == nil {
			return fmt.Errorf("srcs %s: %s is not a symlink but is where the adopted checkout "+
				"belongs in this sandbox — move it aside", filepath.Base(s.RepoRoot), s.Link)
		}
	}
	if err := os.MkdirAll(filepath.Dir(s.Link), 0o755); err != nil {
		return err
	}
	if err := os.Symlink(s.Path, s.Link); err != nil {
		return err
	}
	if w != nil {
		fmt.Fprintf(w, "sandboxer: srcs %s: adopted checkout %s linked into the sandbox at %s\n",
			filepath.Base(s.RepoRoot), s.Path, s.Link)
	}
	return nil
}

// removeLink drops a stale adopted-source link and tidies the branch dirs it
// leaves behind. It removes a SYMLINK and nothing else: anything real at that
// path belongs to someone and is left where it is.
func removeLink(path, root string) {
	if fi, err := os.Lstat(path); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return
	}
	if os.Remove(path) == nil {
		removeEmptyParents(path, root)
	}
}

// warnAdoptedUnreachable reports adopted sources whose in-sandbox link the
// container cannot follow. A narrowed sandbox does not mount <slug>/ at all —
// that absence IS the containment boundary (see Mounts) — so the links inside it
// are not there either, and the adopted tree is reachable only at its own host
// path. Saying so beats letting the user hunt for a source that is mounted but
// not where every other one is.
func warnAdoptedUnreachable(srcs []Source, w io.Writer) {
	if w == nil || !Narrowed(srcs) {
		return
	}
	for _, s := range srcs {
		if s.Link == "" {
			continue
		}
		fmt.Fprintf(w, "sandboxer: srcs %s: adopted — this sandbox is narrowed, so the sandbox root "+
			"is not mounted and %s cannot be followed inside; the source is reachable at %s\n",
			filepath.Base(s.RepoRoot), s.Link, s.Path)
	}
}

// materializeSrc creates the managed worktree for s — or recovers it, when
// git still holds the branch to an earlier checkout. Two recoveries, both
// prompted by git's one-worktree-per-branch rule (a plain `worktree add`
// would refuse with "already used by worktree"):
//   - the branch's worktree sits under a _detached/ root (set aside when the
//     source was dropped or its branch changed, wanted again now): move it
//     back to the managed path — uncommitted work returns with it;
//   - the registered directory no longer exists (removed by hand, not via
//     git): prune the stale registration and check out fresh.
func (b *Base) materializeSrc(s Source, w io.Writer) error {
	if wt, ok := worktree.FindWorktree(s.RepoRoot, s.Branch); ok {
		switch {
		case !dirExists(wt):
			_ = worktree.Prune(s.RepoRoot)
		case isSetAside(wt):
			return b.reattachSrc(wt, s, w)
		}
	}
	return worktree.Ensure(s.RepoRoot, s.Path, s.Branch, w)
}

// reattachSrc moves a set-aside worktree back to its managed path — the reverse
// of detachSrc, keeping any uncommitted work. The sandbox must never depend on a
// _detached/ location: that is exactly what `clean --detached` destroys.
func (b *Base) reattachSrc(from string, s Source, w io.Writer) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	if err := worktree.Move(s.RepoRoot, from, s.Path); err != nil {
		return fmt.Errorf("re-attach set-aside worktree %s: %w", filepath.Base(s.RepoRoot), err)
	}
	_ = os.Remove(filepath.Dir(from)) // tidy the _detached/ dir when now empty
	if w != nil {
		fmt.Fprintf(w, "sandboxer: srcs %s: worktree re-attached from %s (branch %s, work intact)\n",
			filepath.Base(s.RepoRoot), from, s.Branch)
	}
	_, err := worktree.Unsparse(s.Path, w)
	return err
}

// relocateSrc moves a managed worktree to its newly assigned path — same
// source, same branch, only the layout-derived location changed (a layout
// change across sandboxer versions, a dedup rename). Old and new path share
// the worktrees root, so the move is a same-filesystem rename: uncommitted
// work AND git-ignored build caches survive, where a detach-and-recheckout
// would destroy the caches (git considers an ignored-only tree clean).
func (b *Base) relocateSrc(slug string, s Source, to string, w io.Writer) error {
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	if err := worktree.Move(s.RepoRoot, s.Path, to); err != nil {
		return err
	}
	removeEmptyParents(s.Path, b.SandboxDir(slug))
	if w != nil {
		fmt.Fprintf(w, "sandboxer: srcs %s: worktree moved to %s (branch %s, work intact)\n",
			filepath.Base(s.RepoRoot), to, s.Branch)
	}
	return nil
}

// isSetAside reports whether path lies under a _detached/ directory — a
// worktree sandboxer moved aside when its source was dropped or its branch
// changed. The test is by path component, not against this project's own
// detached roots: the set-aside copy may live under an older version's root
// or another project's, and every one of them is a `clean --detached` target
// — a source must never be adopted from there.
func isSetAside(path string) bool {
	for _, seg := range strings.Split(filepath.Clean(path), string(filepath.Separator)) {
		if seg == "_detached" {
			return true
		}
	}
	return false
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// checkViewDirs rejects an include directory that cannot be safely bind-mounted
// as the container's view of the source. It is a hard error, not a warning, for
// three reasons:
//   - an engine asked to bind-mount a MISSING source path CREATES it —
//     root-owned, on the host, inside the user's worktree;
//   - a sandbox whose view is a typo would otherwise come up silently empty;
//   - a view dir that is (or traverses) a SYMLINK pointing OUTSIDE the worktree
//     is a containment ESCAPE: the engine resolves the mount source on the HOST
//     before mounting, so `include = ["/services/"]` where services -> /etc
//     would bind-mount the host's /etc into the container, past the wall. The
//     old sparse-checkout model was immune (a symlink inside the single <slug>/
//     mount resolved in the container's namespace); view mounts are not, so the
//     lexical prefix belt is not enough — the REAL path must stay in the tree.
//
// The message names the branch, since a missing/typo'd path usually exists on
// another one.
func checkViewDirs(s Source) error {
	if config.WholeRepo(s.Include) {
		return nil
	}
	name := filepath.Base(s.RepoRoot)
	sep := string(filepath.Separator)
	// Resolve the worktree root once — the containment base every view dir's
	// real path must fall under. Resolving both sides makes the comparison
	// robust to a symlinked ancestor of the worktree itself (e.g. /tmp).
	root, err := filepath.EvalSymlinks(s.Path)
	if err != nil {
		return fmt.Errorf("srcs %s: %w", name, err)
	}
	detailed, err := viewDirsDetailed(s)
	if err != nil {
		return err
	}
	for _, v := range detailed {
		// ValidateInclude already rejects ".."-style entries; this lexical belt
		// guarantees no include-derived mount PATH leaves the worktree. For
		// expanded pattern dirs the existence/containment checks are a TOCTOU
		// belt — the walker only yields real dirs under the worktree, but the
		// engine mounts them later, so they must still hold NOW.
		if !strings.HasPrefix(v.dir, s.Path+sep) {
			return fmt.Errorf("srcs %s: include %q escapes the worktree", name, v.pattern)
		}
		if !dirExists(v.dir) {
			return fmt.Errorf("srcs %s: include %q is not a directory on branch %s — "+
				"check the path (sandboxer config edit); only directories that exist can be exposed",
				name, v.pattern, s.Branch)
		}
		// Real-path belt: a symlinked view dir (or a symlinked component) must
		// not resolve OUTSIDE the worktree — otherwise the engine bind-mounts the
		// host target past the wall. Symlinks INSIDE the mounted content are fine:
		// the container dereferences those in its own namespace.
		real, err := filepath.EvalSymlinks(v.dir)
		if err != nil {
			return fmt.Errorf("srcs %s: include %q: %w", name, v.pattern, err)
		}
		if real != root && !strings.HasPrefix(real, root+sep) {
			return fmt.Errorf("srcs %s: include %q resolves to %q, outside the worktree — a symlinked "+
				"include would bind-mount host files past the sandbox boundary; expose a real directory",
				name, v.pattern, real)
		}
	}
	return nil
}

// detachSrc takes a managed worktree out of the mounted <slug>/ tree when its
// source was dropped (or its branch changed). Removing it from the tree is
// mandatory — a tree that stays would remain visible in the container,
// silently keeping access open. What happens to it depends on what it holds:
// a CLEAN worktree is removed outright (its commits live on the branch, which
// is always kept — nothing is lost), one with uncommitted work moves to
// _detached/ with the work intact. A directory git no longer recognizes as a
// worktree is set aside the same way (its contents may be real work behind a
// broken linkage); only a missing or empty one is removed.
func (b *Base) detachSrc(slug string, s Source, w io.Writer) error {
	defer removeEmptyParents(s.Path, b.SandboxDir(slug))
	name := filepath.Base(s.RepoRoot) // display name: the repo, not the branch leaf
	if !worktree.IsWorktree(s.Path) {
		entries, err := os.ReadDir(s.Path)
		switch {
		case os.IsNotExist(err):
			return nil // already gone
		case err != nil:
			return fmt.Errorf("set dropped source %s aside: %w", name, err)
		case len(entries) == 0:
			_ = os.RemoveAll(s.Path) // empty — nothing to preserve
			return nil
		}
		target, err := b.detachTarget(slug, s)
		if err != nil {
			return err
		}
		if err := os.Rename(s.Path, target); err != nil {
			return fmt.Errorf("set dropped source %s aside: %w", name, err)
		}
		// The shared repo may still hold this path's worktree ADMIN entry (a
		// squatting repo replaced the pointer file, not the bookkeeping) — and
		// a live entry keeps its branch "checked out", blocking a future
		// checkout. The rename above broke the entry's link, so prune drops
		// exactly it; best-effort, the set-aside itself succeeded.
		_ = worktree.Prune(s.RepoRoot)
		if w != nil {
			fmt.Fprintf(w, "sandboxer: source %s dropped — its directory moved to %s (not a git worktree)\n",
				name, target)
		}
		return nil
	}
	if !worktree.HasWork(s.Path) {
		if err := worktree.Remove(s.RepoRoot, s.Path); err != nil {
			return fmt.Errorf("remove dropped source %s: %w", name, err)
		}
		if w != nil {
			fmt.Fprintf(w, "sandboxer: source %s dropped — clean worktree removed (branch %s kept)\n",
				name, s.Branch)
		}
		return nil
	}
	target, err := b.detachTarget(slug, s)
	if err != nil {
		return err
	}
	if err := worktree.Move(s.RepoRoot, s.Path, target); err != nil {
		return fmt.Errorf("set dropped source %s aside: %w", name, err)
	}
	if w != nil {
		fmt.Fprintf(w, "sandboxer: source %s dropped — uncommitted work moved to %s (branch %s kept)\n",
			name, target, s.Branch)
	}
	return nil
}

// removeEmptyParents removes the now-empty directories between path's parent
// and root (exclusive) — the branch-derived intermediates a worktree
// (devops/branch1/repo → devops/branch1/ → devops/) leaves behind when it
// moves out. os.Remove refuses a non-empty dir, so the walk stops at the
// first one still in use.
func removeEmptyParents(path, root string) {
	for dir := filepath.Dir(path); strings.HasPrefix(dir, root+string(filepath.Separator)); dir = filepath.Dir(dir) {
		if os.Remove(dir) != nil {
			return
		}
	}
}

// CleanDetached removes everything set aside under _detached/ — under the
// default sibling root, every sandbox's configured worktreesDir, and the
// legacy state-dir location — and prunes the affected repos' worktree admin
// entries. Live sandboxes are not touched, and branches are kept (they live
// in the repos): only the set-aside working trees, uncommitted work included,
// are destroyed — the caller gates this behind an explicit confirmation.
// Returns the roots actually removed.
func (b *Base) CleanDetached() ([]string, error) {
	var removed []string
	repos := map[string]bool{}
	for _, root := range b.detachedRoots() {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue // absent — nothing set aside there
		}
		for _, e := range entries {
			// A set-aside worktree knows its repo via --git-common-dir
			// (<repo>/.git); a plain renamed dir has none and needs no prune.
			if _, common, ok := worktree.Detect(filepath.Join(root, e.Name())); ok {
				repos[filepath.Dir(common)] = true
			}
		}
		if err := os.RemoveAll(root); err != nil {
			return removed, err
		}
		removed = append(removed, root)
	}
	for r := range repos {
		_ = worktree.Prune(r)
	}
	return removed, nil
}

// ensureIgnored makes sure a worktrees root that lives INSIDE the project is
// git-ignored there, so sandbox working copies can never land in a commit —
// the repo-hygiene invariant behind the old outside-the-repo default. The
// entry is appended to the project's .gitignore (created if absent) once;
// a root outside the project needs nothing.
func (b *Base) ensureIgnored(root string, w io.Writer) error {
	rel, err := filepath.Rel(b.Src, root)
	if err != nil {
		rel = ".." // no relative form (another volume) — treat as outside
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil // outside the project (or the project itself) — nothing to ignore
	}
	entry := "/" + filepath.ToSlash(rel) + "/"
	gi := filepath.Join(b.Src, ".gitignore")
	data, _ := os.ReadFile(gi) // absent reads as empty — created below
	for _, line := range strings.Split(string(data), "\n") {
		switch strings.TrimSpace(line) {
		case entry, strings.TrimSuffix(entry, "/"), strings.TrimPrefix(entry, "/"),
			strings.Trim(entry, "/"):
			return nil // already covered
		}
	}
	out := string(data)
	if out != "" && !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	out += entry + "\n"
	if err := os.WriteFile(gi, []byte(out), 0o644); err != nil {
		return fmt.Errorf("git-ignore the worktrees dir: %w", err)
	}
	if w != nil {
		fmt.Fprintf(w, "sandboxer: added %s to %s (worktrees must never be committed)\n", entry, gi)
	}
	return nil
}

// detachedRoots enumerates every _detached/ location the project may have
// accumulated: the default in-project root, each sandbox's configured
// worktreesDir, the legacy sibling root, and the legacy state-dir one.
func (b *Base) detachedRoots() []string {
	seen := map[string]bool{}
	roots := []string{
		filepath.Join(SandboxesRoot(b.Src), "_detached"),
		filepath.Join(legacySiblingRoot(b.Src), "_detached"),
		filepath.Join(b.Dir, "_detached"),
	}
	for _, r := range roots {
		seen[r] = true
	}
	for _, slug := range b.Agents() {
		if r := b.detachedDir(slug); !seen[r] {
			seen[r] = true
			roots = append(roots, r)
		}
	}
	return roots
}

// detachTarget picks a fresh destination under _detached/ for slug's dropped
// source (creating the dir), suffix-numbered on collision.
func (b *Base) detachTarget(slug string, s Source) (string, error) {
	if err := os.MkdirAll(b.detachedDir(slug), 0o755); err != nil {
		return "", err
	}
	target := filepath.Join(b.detachedDir(slug), slug+"-"+filepath.Base(s.Path))
	for i := 2; ; i++ {
		if _, err := os.Stat(target); os.IsNotExist(err) {
			break
		}
		target = filepath.Join(b.detachedDir(slug), fmt.Sprintf("%s-%s-%d", slug, filepath.Base(s.Path), i))
	}
	return target, nil
}

// Narrowed reports whether any source restricts what the container sees of it.
// It is the switch between the sandbox's two mount shapes (see Mounts): with no
// narrowed source the container gets the <slug>/ root whole, exactly as it
// always has; one narrowed source moves every source onto its own mount.
func Narrowed(srcs []Source) bool {
	for _, s := range srcs {
		if !config.WholeRepo(s.Include) {
			return true
		}
	}
	return false
}

// viewDir pairs a resolved view directory with the include entry that produced
// it, so a diagnostic can name what the user actually wrote (a pattern may
// resolve to many directories, so an index into Include no longer works).
type viewDir struct {
	pattern string // the raw include entry
	dir     string // abs host directory under s.Path
}

// viewDirsDetailed resolves a narrowed source's include entries to concrete
// host directories. A literal entry maps lexically — no disk access, exactly
// the pre-pattern behavior (existence is checkViewDirs' job, with its original
// message). A pattern entry is expanded against the worktree on disk and must
// select at least one directory: fail closed — a pattern matching nothing
// would otherwise come up as a silently empty sandbox, the very failure mode
// checkViewDirs exists to prevent.
func viewDirsDetailed(s Source) ([]viewDir, error) {
	name := filepath.Base(s.RepoRoot)
	out := make([]viewDir, 0, len(s.Include))
	for _, p := range s.Include {
		if !includeIsPattern(p) {
			out = append(out, viewDir{pattern: p, dir: filepath.Join(s.Path, filepath.FromSlash(strings.Trim(p, "/")))})
			continue
		}
		dirs, err := expandInclude(s.Path, p)
		if err != nil {
			return nil, fmt.Errorf("srcs %s: include %q: %w", name, p, err)
		}
		if len(dirs) == 0 {
			return nil, fmt.Errorf("srcs %s: include %q matches no directory on branch %s — patterns select "+
				"directories, never files (a file cannot be mounted alone); check it with: sandboxer config edit",
				name, p, s.Branch)
		}
		for _, d := range dirs {
			out = append(out, viewDir{pattern: p, dir: d})
		}
	}
	return out, nil
}

// ViewDirs returns the absolute host directories of s the container may see:
// the include directories (patterns expanded against the worktree on disk) for
// a narrowed source, else the worktree itself. These are bind-mounted at their
// own paths, so what is NOT listed is not mounted and therefore does not exist
// inside the container — the host tree stays complete regardless (that is the
// point: an IDE can open it). It errors when a pattern matches nothing or the
// worktree cannot be walked.
func ViewDirs(s Source) ([]string, error) {
	if config.WholeRepo(s.Include) {
		return []string{s.Path}, nil
	}
	detailed, err := viewDirsDetailed(s)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(detailed))
	for i, v := range detailed {
		out[i] = v.dir
	}
	return out, nil
}

// Mounts returns the container's source bind mounts and whether the sandbox's
// <slug>/ root is mounted as one.
//
// Nothing narrowed: the root IS the mount — one stable window whose contents a
// live session sees change (the pre-view behavior, kept byte-identical so an
// unnarrowed sandbox's argv, and its session hash, never moved).
//
// Anything narrowed: the root is NOT mounted and every source is mounted
// individually. This is the whole containment boundary — the excluded files sit
// on the host inside an unmounted directory, so they are unreachable from the
// container rather than merely unreadable. Skipping the root mount is therefore
// load-bearing, not an optimization; TestMounts_NarrowedNeverMountsDest pins it.
// Sorted and de-duplicated for a stable, minimal argv (the session ConfigHash
// depends on it): an exact-duplicate include (or a child listed twice) must not
// emit the same --volume twice — a nested parent+child stays as two DISTINCT
// paths, only literal repeats collapse.
//
// Include patterns are expanded against the live worktree on every call, so
// the resolved set — and with it the argv and the session hash — tracks the
// host: a directory created on the host that matches a pattern makes the
// session read as stale on the next enter, exactly like MountFingerprint
// tracks inode moves. Whether that stale verdict rebuilds the session or
// merely reports itself is the CLI's call, not this one (sessions-design D3).
// Only the narrowed branch can error (pattern matching nothing, unreadable
// worktree); adopted and unnarrowed sources are literal paths.
func Mounts(srcs []Source) (mountDest bool, mounts []string, err error) {
	if !Narrowed(srcs) {
		for _, s := range srcs {
			if !s.Managed { // adopted worktrees live outside <slug>/
				mounts = append(mounts, s.Path)
			}
		}
		return true, sortedUnique(mounts), nil
	}
	for _, s := range srcs {
		dirs, err := ViewDirs(s)
		if err != nil {
			return false, nil, err
		}
		mounts = append(mounts, dirs...)
	}
	return false, sortedUnique(mounts), nil
}

// sortedUnique sorts paths and drops exact duplicates (nil in → nil out).
func sortedUnique(paths []string) []string {
	if len(paths) == 0 {
		return paths
	}
	sort.Strings(paths)
	out := paths[:1]
	for _, p := range paths[1:] {
		if p != out[len(out)-1] {
			out = append(out, p)
		}
	}
	return out
}

// MountFingerprint fingerprints the on-disk identity of individual source
// mounts, for RunOpts.MountGen — see it for the why. It is the view-mount
// analogue of Gen: the <slug>/ root mount is inode-stable (a git operation
// inside it recreates children, never the mounted dir itself), but the
// directories mounted one level in — a narrowed sandbox's view dirs, an adopted
// worktree — are exactly what a host-side checkout/rebase can remove and
// recreate, orphaning a live session's bind mount.
//
// The fingerprint is device+inode per mount, in the given order (Mounts sorts
// them), so it is STABLE across syncs when nothing external changed and flips
// the moment a mounted directory becomes a different inode. Empty in →  empty
// out: a sandbox whose only mount is the inode-stable <slug>/ root needs no
// fingerprint, and the empty value keeps its argv, and session hash, unchanged.
// A path that cannot be stat'd contributes a sentinel rather than being
// skipped, so a directory vanishing also flips the hash.
//
// Split in two (MountIdentities + FingerprintIDs) so the identities themselves
// can be recorded on the session and diffed later — the hash alone says THAT
// something moved, never what. See mountid.go.
func MountFingerprint(mounts []string) string {
	return FingerprintIDs(MountIdentities(mounts))
}

// inodeID returns a string identifying the file OBJECT at path (see
// statIdentity for what it is and why), or "missing" when it cannot be stat'd —
// a stable sentinel so a vanished mount still changes the fingerprint
// deterministically.
func inodeID(path string) string {
	if id, ok := statIdentity(path); ok {
		return id
	}
	return "missing"
}
