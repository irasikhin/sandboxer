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
	// AutoBranch marks the managed feat/<slug>-sb branch (no explicit branch:
	// in the config); recreate --full deletes only these, never a branch the
	// user named.
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
// of the mounted <slug>/ tree so the container loses access. `clean` wipes it
// with the rest of the state dir.
func (b *Base) detachedDir() string { return filepath.Join(b.Dir, "_detached") }

// profileSrcs reads the srcs list from the sandbox's stored profile.json.
// There is no implicit default — an absent profile or empty list is rejected
// by resolveSrcs (the scaffolded config seeds an explicit srcs: [{src: .}]).
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
// commit, no repo twice), branches decided (explicit branch: adopts an
// existing worktree when one exists; otherwise the managed feat/<slug>-sb),
// and managed worktrees named under <slug>/ (repo basename, deduped by a
// short path hash on collision). Entry order is preserved. An empty list is
// an error, never an implicit "current directory": what a sandbox exposes is
// always spelled out in the config.
func (b *Base) resolveSrcs(slug string, specs []config.Src, w io.Writer) ([]Source, error) {
	if len(specs) == 0 {
		return nil, fmt.Errorf("srcs is empty — a sandbox needs at least one source; add to %s, e.g.:\n"+
			"  srcs:\n"+
			"    - src: .               # this repo\n"+
			"    - src: ../other-repo   # any git repo\n"+
			"(edit it with: sandboxer config edit)", config.ConfigFileName)
	}
	seenRepo := map[string]bool{}
	seenName := map[string]bool{}
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
		if spec.Branch != "" {
			src.Branch = spec.Branch
			if wt, ok := worktree.FindWorktree(top, spec.Branch); ok {
				// Adopt the existing checkout of that branch as-is.
				src.Path = wt
				if len(spec.Include) > 0 && w != nil {
					fmt.Fprintf(w, "sandboxer: srcs %s: include ignored — adopting the existing worktree at %s as-is\n",
						filepath.Base(top), wt)
				}
				src.Include = nil
				out = append(out, src)
				continue
			}
		} else {
			src.Branch = worktree.Branch(slug)
			src.AutoBranch = true
		}
		src.Managed = true
		name := filepath.Base(top)
		if seenName[name] {
			name = name + "-" + shortPathHash(top)
		}
		seenName[name] = true
		src.Path = filepath.Join(b.SandboxDir(slug), name)
		out = append(out, src)
	}
	return out, nil
}

// shortPathHash disambiguates two source repos that share a base name.
func shortPathHash(p string) string {
	sum := sha256.Sum256([]byte(p))
	return hex.EncodeToString(sum[:])[:4]
}

// SyncSrcs converges the sandbox's on-disk sources onto the stored profile:
// it (re-)resolves srcs, creates missing managed worktrees, re-syncs their
// sparse patterns, sets aside managed worktrees whose source was dropped (or
// whose branch changed) under _detached/, and records the result in
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
	srcs, err := b.resolveSrcs(slug, b.profileSrcs(slug), w)
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
	for _, prev := range b.Srcs(slug) {
		if !prev.Managed {
			continue
		}
		if br, ok := want[prev.Path]; ok && br == worktree.CurrentBranch(prev.Path) {
			continue
		}
		if _, ok := want[prev.Path]; ok && !worktree.IsWorktree(prev.Path) {
			continue // not materialized yet; Ensure below will create it
		}
		if err := b.detachSrc(slug, prev, w); err != nil {
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
// container, silently keeping access open.
func (b *Base) detachSrc(slug string, s Source, w io.Writer) error {
	if !worktree.IsWorktree(s.Path) {
		_ = os.RemoveAll(s.Path)
		return nil
	}
	if err := os.MkdirAll(b.detachedDir(), 0o755); err != nil {
		return err
	}
	target := filepath.Join(b.detachedDir(), slug+"-"+filepath.Base(s.Path))
	for i := 2; ; i++ {
		if _, err := os.Stat(target); os.IsNotExist(err) {
			break
		}
		target = filepath.Join(b.detachedDir(), fmt.Sprintf("%s-%s-%d", slug, filepath.Base(s.Path), i))
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
