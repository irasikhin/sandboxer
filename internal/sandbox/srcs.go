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
// the sandboxes root (same filesystem as the worktrees, so setting one aside
// is always a rename); `clean` wipes it with the rest.
func (b *Base) detachedDir() string { return filepath.Join(b.SandboxesRoot(), "_detached") }

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
			!strings.HasPrefix(wt, b.SandboxDir(slug)+string(filepath.Separator)) {
			// The branch is already checked out somewhere OUTSIDE this sandbox
			// (your own worktree, or the repo's main checkout): adopt that
			// checkout as-is — git allows only one worktree per branch. This
			// sandbox's own managed worktree is not "somewhere": it stays
			// managed, or a re-sync would adopt it and drop its include.
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

// assignManagedPaths places each managed source's worktree under root, named
// by its branch — path = <root>/<branch>, branch slashes becoming directories
// — so the on-disk layout spells out sandbox and branch (…/mybox/devops/x).
// Sources on the SAME branch name (different repos) each get <branch>/<repo>
// instead (one path cannot be both a checkout and a parent), deduped by a
// short path hash when the repos share a basename too. Branch names that nest
// across sources (one a path prefix of another) cannot coexist on disk and
// are rejected.
func assignManagedPaths(root string, srcs []Source) error {
	uses := map[string]int{}
	for _, s := range srcs {
		if s.Managed {
			uses[s.Branch]++
		}
	}
	seenName := map[string]bool{}
	sep := string(filepath.Separator)
	for i := range srcs {
		if !srcs[i].Managed {
			continue
		}
		rel := filepath.FromSlash(srcs[i].Branch)
		if uses[srcs[i].Branch] > 1 {
			name := filepath.Base(srcs[i].RepoRoot)
			if seenName[srcs[i].Branch+"\x00"+name] {
				name = name + "-" + shortPathHash(srcs[i].RepoRoot)
			}
			seenName[srcs[i].Branch+"\x00"+name] = true
			rel = filepath.Join(rel, name)
		}
		p := filepath.Join(root, rel)
		// ValidBranch already rejects ".."-style names; this is the belt that
		// guarantees no branch-derived path ever leaves the sandbox dir.
		if !strings.HasPrefix(p, root+sep) {
			return fmt.Errorf("srcs branch %q escapes the sandbox directory", srcs[i].Branch)
		}
		srcs[i].Path = p
	}
	for i := range srcs {
		for j := range srcs {
			if i == j || !srcs[i].Managed || !srcs[j].Managed {
				continue
			}
			if strings.HasPrefix(srcs[j].Path, srcs[i].Path+sep) {
				return fmt.Errorf("srcs branches %q and %q nest on disk — one worktree would sit "+
					"inside the other; rename one", srcs[i].Branch, srcs[j].Branch)
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
// resolveSrcs), creates missing managed worktrees, re-syncs their sparse
// patterns, sets aside managed worktrees whose source was dropped (or whose
// branch: changed) under _detached/, and records the result in
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
	if err := os.MkdirAll(dest, 0o755); err != nil {
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
						filepath.Base(p.Path))
				}
				continue
			}
		}
		if err := b.detachSrc(slug, p, w); err != nil {
			return nil, err
		}
	}

	for _, s := range srcs {
		if !s.Managed {
			continue
		}
		if worktree.IsWorktree(s.Path) {
			if _, err := worktree.SyncSparse(s.Path, s.Include, w); err != nil {
				return nil, err
			}
		} else if err := worktree.Ensure(s.RepoRoot, s.Path, s.Branch, s.Include, w); err != nil {
			return nil, err
		}
		warnEmptySelection(s, w)
	}
	return srcs, b.writeSrcs(slug, srcs)
}

// warnEmptySelection notes when a narrowed source materialized no files — a
// typo'd include pattern would otherwise yield a silently empty sandbox.
func warnEmptySelection(s Source, w io.Writer) {
	if w == nil || len(s.Include) == 0 || (len(s.Include) == 1 && s.Include[0] == "**") {
		return
	}
	entries, err := os.ReadDir(s.Path)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Name() != ".git" {
			return
		}
	}
	fmt.Fprintf(w, "sandboxer: srcs %s: include matched no files — check the patterns "+
		"(gitignore syntax; a directory is \"/dir/\")\n", filepath.Base(s.Path))
}

// detachSrc moves a managed worktree out of the mounted <slug>/ tree into
// _detached/, keeping its branch and any uncommitted work intact. The move is
// mandatory — a tree that cannot be set aside would stay visible in the
// container, silently keeping access open. A directory git no longer
// recognizes as a worktree is set aside the same way (its contents may be
// real work behind a broken linkage); only a missing or empty one is removed.
func (b *Base) detachSrc(slug string, s Source, w io.Writer) error {
	defer removeEmptyParents(s.Path, b.SandboxDir(slug))
	if !worktree.IsWorktree(s.Path) {
		entries, err := os.ReadDir(s.Path)
		switch {
		case os.IsNotExist(err):
			return nil // already gone
		case err != nil:
			return fmt.Errorf("set dropped source %s aside: %w", filepath.Base(s.Path), err)
		case len(entries) == 0:
			_ = os.RemoveAll(s.Path) // empty — nothing to preserve
			return nil
		}
		target, err := b.detachTarget(slug, s)
		if err != nil {
			return err
		}
		if err := os.Rename(s.Path, target); err != nil {
			return fmt.Errorf("set dropped source %s aside: %w", filepath.Base(s.Path), err)
		}
		if w != nil {
			fmt.Fprintf(w, "sandboxer: source %s dropped — its directory moved to %s (not a git worktree)\n",
				filepath.Base(s.Path), target)
		}
		return nil
	}
	target, err := b.detachTarget(slug, s)
	if err != nil {
		return err
	}
	if err := worktree.Move(s.RepoRoot, s.Path, target); err != nil {
		return fmt.Errorf("set dropped source %s aside: %w", filepath.Base(s.Path), err)
	}
	if w != nil {
		fmt.Fprintf(w, "sandboxer: source %s dropped — its worktree moved to %s (branch %s kept)\n",
			filepath.Base(s.Path), target, s.Branch)
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

// detachTarget picks a fresh destination under _detached/ for slug's dropped
// source (creating the dir), suffix-numbered on collision.
func (b *Base) detachTarget(slug string, s Source) (string, error) {
	if err := os.MkdirAll(b.detachedDir(), 0o755); err != nil {
		return "", err
	}
	target := filepath.Join(b.detachedDir(), slug+"-"+filepath.Base(s.Path))
	for i := 2; ; i++ {
		if _, err := os.Stat(target); os.IsNotExist(err) {
			break
		}
		target = filepath.Join(b.detachedDir(), fmt.Sprintf("%s-%s-%d", slug, filepath.Base(s.Path), i))
	}
	return target, nil
}

// SrcMounts returns the extra container mounts a sandbox needs beyond its
// <slug>/ root: the adopted worktrees, which live outside the state dir.
// Sorted for a stable argv (the session ConfigHash depends on it).
func SrcMounts(srcs []Source) []string {
	var out []string
	for _, s := range srcs {
		if !s.Managed {
			out = append(out, s.Path)
		}
	}
	sort.Strings(out)
	return out
}
