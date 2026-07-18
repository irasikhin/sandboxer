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
	Include  []string `json:"include,omitempty"` // gitignore-style narrowing patterns
	// Managed marks a worktree sandboxer created under <slug>/ and may tear
	// down. An adopted worktree (a srcs entry whose branch: was already
	// checked out somewhere, including the repo's main checkout) is never
	// touched by teardown — it is only mounted.
	Managed bool `json:"managed"`
	// AutoBranch marks a branch sandboxer MINTED itself (it did not exist at
	// first sync); recreate --full deletes only these — a branch that existed
	// before the sandbox is never deleted.
	AutoBranch bool `json:"autoBranch,omitempty"`
}

// Name is the label this source is addressed by within its sandbox: the
// repo-level directory of its worktree (<slug>/<NAME>/<branch> — deduped by
// assignManagedPaths when two repos share a basename), or the checkout's own
// dir name for an adopted source. Unique across a sandbox's managed sources.
// `path <slug> <name>` selects one source by it.
func (s Source) Name() string {
	if suffix := string(filepath.Separator) + filepath.FromSlash(s.Branch); s.Managed && strings.HasSuffix(s.Path, suffix) {
		return filepath.Base(strings.TrimSuffix(s.Path, suffix))
	}
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
// worktrees are placed under <slug>/ named by their branch (see
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
			// The branch is already checked out somewhere OUTSIDE this sandbox
			// (your own worktree, or the repo's main checkout): adopt that
			// checkout as-is — git allows only one worktree per branch. This
			// sandbox's own managed worktree is not "somewhere": it stays
			// managed, or a re-sync would adopt it and drop its include. Nor is
			// a checkout set aside under _detached/ or one whose directory is
			// gone (a stale registration): those stay managed too —
			// materializeSrc re-attaches the one and prunes the other.
			src.Path = wt
			src.AutoBranch = false
			if len(spec.Include) > 0 && w != nil {
				fmt.Fprintf(w, "sandboxer: srcs %s: include ignored — adopting the existing worktree at %s as-is\n",
					filepath.Base(top), wt)
			}
			src.Include = nil
			out = append(out, src)
			continue
		}
		src.Managed = true
		out = append(out, src)
	}
	if err := assignManagedPaths(b.SandboxDir(slug), out); err != nil {
		return nil, err
	}
	return out, nil
}

// assignManagedPaths places each managed source's worktree under root,
// grouped by repo and named by branch — path = <root>/<repo>/<branch>, branch
// slashes becoming directories — so the on-disk layout spells out repo and
// branch (…/mybox/miko-java/feat/BDP-5291). Repos sharing a basename are
// deduped with a short path hash. Within one repo branch names cannot nest
// (git forbids such refs) and a repo appears once per sandbox, so paths never
// collide.
func assignManagedPaths(root string, srcs []Source) error {
	seenName := map[string]bool{}
	sep := string(filepath.Separator)
	for i := range srcs {
		if !srcs[i].Managed {
			continue
		}
		name := filepath.Base(srcs[i].RepoRoot)
		if seenName[name] {
			name = name + "-" + shortPathHash(srcs[i].RepoRoot)
		}
		seenName[name] = true
		p := filepath.Join(root, name, filepath.FromSlash(srcs[i].Branch))
		// ValidBranch already rejects ".."-style names; this is the belt that
		// guarantees no branch-derived path ever leaves the sandbox dir.
		if !strings.HasPrefix(p, root+sep) {
			return fmt.Errorf("srcs branch %q escapes the sandbox directory", srcs[i].Branch)
		}
		srcs[i].Path = p
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
// resolveSrcs), creates missing managed worktrees, widens any left narrow by a
// pre-view-mounts sandboxer (Unsparse), sets aside managed worktrees whose
// source was dropped (or whose branch: changed) under _detached/, and records
// the result in
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
	// resolved source (dropped from srcs, renamed, or switched branch).
	want := map[string]string{} // managed path -> branch
	for _, s := range srcs {
		if s.Managed {
			want[s.Path] = s.Branch
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
		if err := b.detachSrc(slug, p, w); err != nil {
			return nil, err
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
		}
		if err := checkViewDirs(s); err != nil {
			return nil, err
		}
	}
	return srcs, b.writeSrcs(slug, srcs)
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
	for i, dir := range ViewDirs(s) {
		// ValidateInclude already rejects ".."-style patterns; this lexical belt
		// guarantees no include-derived mount PATH leaves the worktree.
		if !strings.HasPrefix(dir, s.Path+sep) {
			return fmt.Errorf("srcs %s: include %q escapes the worktree", name, s.Include[i])
		}
		if !dirExists(dir) {
			return fmt.Errorf("srcs %s: include %q is not a directory on branch %s — "+
				"check the path (sandboxer config edit); only directories that exist can be exposed",
				name, s.Include[i], s.Branch)
		}
		// Real-path belt: a symlinked view dir (or a symlinked component) must
		// not resolve OUTSIDE the worktree — otherwise the engine bind-mounts the
		// host target past the wall. Symlinks INSIDE the mounted content are fine:
		// the container dereferences those in its own namespace.
		real, err := filepath.EvalSymlinks(dir)
		if err != nil {
			return fmt.Errorf("srcs %s: include %q: %w", name, s.Include[i], err)
		}
		if real != root && !strings.HasPrefix(real, root+sep) {
			return fmt.Errorf("srcs %s: include %q resolves to %q, outside the worktree — a symlinked "+
				"include would bind-mount host files past the sandbox boundary; expose a real directory",
				name, s.Include[i], real)
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
// and root (exclusive) — the intermediates a branch-named worktree
// (devops/branch1 → devops/) leaves behind when it moves out. os.Remove
// refuses a non-empty dir, so the walk stops at the first one still in use.
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

// ViewDirs returns the absolute host directories of s the container may see:
// the include directories for a narrowed source, else the worktree itself.
// These are bind-mounted at their own paths, so what is NOT listed is not
// mounted and therefore does not exist inside the container — the host tree
// stays complete regardless (that is the point: an IDE can open it).
func ViewDirs(s Source) []string {
	if config.WholeRepo(s.Include) {
		return []string{s.Path}
	}
	out := make([]string, 0, len(s.Include))
	for _, p := range s.Include {
		out = append(out, filepath.Join(s.Path, filepath.FromSlash(strings.Trim(p, "/"))))
	}
	return out
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
func Mounts(srcs []Source) (mountDest bool, mounts []string) {
	if !Narrowed(srcs) {
		for _, s := range srcs {
			if !s.Managed { // adopted worktrees live outside <slug>/
				mounts = append(mounts, s.Path)
			}
		}
		return true, sortedUnique(mounts)
	}
	for _, s := range srcs {
		mounts = append(mounts, ViewDirs(s)...)
	}
	return false, sortedUnique(mounts)
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
func MountFingerprint(mounts []string) string {
	if len(mounts) == 0 {
		return ""
	}
	h := sha256.New()
	for _, m := range mounts {
		fmt.Fprintf(h, "%s\x00%s\x00", m, inodeID(m))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
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
