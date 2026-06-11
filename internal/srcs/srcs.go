// Package srcs implements depsync-style dependency vendoring between a set of
// search roots and a sandbox directory: it locates each dep by path suffix
// under the roots and pulls it into the sandbox (CopyIn), then pushes the
// read-write copies back over their origins (CopyOut).
//
// The copy semantics mirror the depsync reference script, with one safety
// addition on top:
//   - pull KEEPs a target that already exists (unless --force) and records a
//     signature of each origin in the manifest;
//   - push SKIPs an origin that changed on the host since that pull (unless
//     --force), so an out-of-band host edit is never silently overwritten;
//   - copy replaces the destination wholesale and preserves symlinks, file
//     modes and mtimes.
package srcs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Profile is the resolved sandbox configuration: search roots, the deps to
// find under them, and the agent-context files to copy to the sandbox root.
type Profile struct {
	Roots   []string `json:"roots"`
	Deps    []string `json:"deps"`
	Context []string `json:"context"`
}

// DefaultContext is the agent-context set used when the profile has no
// context: list — the common instruction files coding agents read from a
// project root. Entries missing from the project are silently skipped.
var DefaultContext = []string{"CLAUDE.md", "AGENTS.md", ".claude"}

// ManifestEntry records one copied target so a later push can find its origin.
// OriginSig is the origin's signature at pull time (rw entries only): push
// compares it against the live origin and skips on mismatch, so host edits made
// after the pull are never silently overwritten.
type ManifestEntry struct {
	Mode        string `json:"mode"`
	Origin      string `json:"origin"`
	SandboxPath string `json:"sandboxPath"`
	OriginSig   string `json:"originSig,omitempty"`
}

// PullOpts parameterizes CopyIn.
type PullOpts struct {
	ProfileFile  string // resolved profile JSON (roots+deps+context)
	SandboxDir   string
	ManifestFile string
	ProjectRoot  string // project root the context entries are copied from ("" = no context)
	Force        bool   // overwrite sandbox targets that already exist
	InContainer  bool   // in-container pull: host roots aren't mounted (see resolveDeps)
}

// PushOpts parameterizes CopyOut.
type PushOpts struct {
	DryRun bool // report only, touch nothing
	Force  bool // overwrite origins even when they changed on the host since pull
}

// WorkspaceDir is the sandbox subdirectory the deps are vendored into. It
// keeps the working data apart from the sandbox root, which stays free for
// sandbox-level files (e.g. agent context like CLAUDE.md).
const WorkspaceDir = "workspace"

// target is a flattened copy job: origin -> dest with a mode.
type target struct {
	Origin string
	Dest   string
	Mode   string
}

// skip holds directory names never walked into when searching.
var skip = map[string]bool{
	".git":         true,
	"node_modules": true,
	".sandboxer":   true,
}

// walk yields every entry path under root (skipping SKIP names), recursing into
// directories while depth==0 (unlimited) or cur+1 < depth. Unreadable dirs are
// silently ignored.
func walk(root string, depth int, fn func(path string)) {
	walkAt(root, depth, 0, fn)
}

func walkAt(root string, depth, cur int, fn func(path string)) {
	ents, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range ents {
		if skip[e.Name()] {
			continue
		}
		p := filepath.Join(root, e.Name())
		fn(p)
		if e.IsDir() && (depth == 0 || cur+1 < depth) {
			walkAt(p, depth, cur+1, fn)
		}
	}
}

// resolveDeps turns the roots+deps config into copy jobs: each dep is searched
// (by path suffix) under every root — the cwd is always an implicit last root —
// and the first match is copied to <sandbox>/workspace/<dep>. Not-found and
// multi-match are reported to w. When inContainer
// is set, roots that aren't visible on this filesystem trigger an upfront hint —
// host roots aren't bind-mounted into the sandbox, so an in-container pull can
// only refresh deps already vendored on the host.
func resolveDeps(p Profile, sandboxDir string, w io.Writer, inContainer bool) []target {
	if len(p.Deps) == 0 {
		return nil
	}
	ws := filepath.Join(sandboxDir, WorkspaceDir)
	// The cwd is ALWAYS a search root, appended after the explicit roots (which
	// win the deterministic first-match) — so a project profile can list deps
	// from the project itself without any roots: stanza. Dedup by absolute path
	// keeps an explicit `roots: [.]` from double-matching every dep.
	roots := append(slices.Clone(p.Roots), cwd())
	seen := map[string]bool{}
	roots = slices.DeleteFunc(roots, func(r string) bool {
		abs := mustAbs(r)
		if seen[abs] {
			return true
		}
		seen[abs] = true
		return false
	})
	warnUnmountedRoots(w, roots, inContainer)
	var out []target
	for _, dep := range p.Deps {
		dest := absJoin(ws, dep)
		// Never let a dep (absolute, or one with ../ segments) land outside the
		// workspace dir — that would have copy_in write over arbitrary host
		// paths, or clobber sandbox-root files like the agent context.
		if !within(ws, dest) {
			fmt.Fprintf(w, "  SKIP  %s — refusing to copy outside the sandbox workspace (absolute or ../ path)\n", dep)
			continue
		}
		matches := searchDep(roots, dep)
		switch {
		case len(matches) == 0:
			fmt.Fprintf(w, "  SKIP  %s — not found under roots\n", dep)
			continue
		case len(matches) > 1:
			fmt.Fprintf(w, "  WARN  %s — %d matches under roots, using %s (others: %s)\n",
				dep, len(matches), matches[0], strings.Join(matches[1:], ", "))
		}
		out = append(out, target{
			Origin: matches[0],
			Dest:   dest,
			Mode:   "rw",
		})
	}
	return out
}

// resolveContext turns the profile's context list (or DefaultContext when none
// is set) into read-only copy jobs from the project root to the SANDBOX ROOT —
// beside workspace/, where agents discover instruction files like CLAUDE.md.
// ro entries are never pushed back. A missing default entry is silently
// skipped; a missing explicit entry warns. Entries may not escape the sandbox
// root or shadow the workspace dir.
func resolveContext(p Profile, projectRoot, sandboxDir string, w io.Writer) []target {
	if projectRoot == "" {
		return nil
	}
	list := p.Context
	explicit := len(list) > 0
	if !explicit {
		list = DefaultContext
	}
	ws := filepath.Join(sandboxDir, WorkspaceDir)
	var out []target
	for _, rel := range list {
		dest := absJoin(sandboxDir, rel)
		if !within(sandboxDir, dest) || within(ws, dest) {
			fmt.Fprintf(w, "  SKIP  context %s — must stay at the sandbox root (no absolute/../ paths, not under %s/)\n", rel, WorkspaceDir)
			continue
		}
		origin := filepath.Join(projectRoot, rel)
		if !exists(origin) {
			if explicit {
				fmt.Fprintf(w, "  SKIP  context %s — not found under %s\n", rel, projectRoot)
			}
			continue
		}
		out = append(out, target{Origin: origin, Dest: dest, Mode: "ro"})
	}
	return out
}

// warnUnmountedRoots prints a single hint when, inside the container, one or more
// search roots do not exist on the local filesystem. Host roots are never
// bind-mounted into the sandbox, so those deps can't be discovered in here — they
// must be pulled on the host. Outside the container this is a no-op.
func warnUnmountedRoots(w io.Writer, roots []string, inContainer bool) {
	if !inContainer {
		return
	}
	var missing []string
	for _, r := range roots {
		if _, err := os.Stat(mustAbs(r)); err != nil {
			missing = append(missing, r)
		}
	}
	if len(missing) == 0 {
		return
	}
	fmt.Fprintf(w, "  NOTE  host roots aren't mounted inside the sandbox: %s\n"+
		"        run `sandboxer pull` on the host (in here only deps already vendored on the host refresh)\n",
		strings.Join(missing, ", "))
}

// searchDep finds entries under roots whose path ends with dep's components
// (depsync's leaf-name + path-suffix match), limited to depth 5. Matches are
// returned in deterministic (sorted-walk) order.
func searchDep(roots []string, dep string) []string {
	depParts := strings.Split(strings.Trim(filepath.ToSlash(dep), "/"), "/")
	leaf := depParts[len(depParts)-1]
	var found []string
	for _, r := range roots {
		root := mustAbs(r)
		walk(root, 5, func(p string) {
			if filepath.Base(p) != leaf {
				return
			}
			parts := strings.Split(filepath.ToSlash(p), "/")
			if len(parts) >= len(depParts) && slices.Equal(parts[len(parts)-len(depParts):], depParts) {
				found = append(found, p)
			}
		})
	}
	return found
}

// copyEntry copies src onto dst the way depsync's copy_entry does: dst is
// removed first, then src is copied — symlinks preserved, directories recursed,
// file mode and mtime carried over.
func copyEntry(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	return copyTree(src, dst)
}

func copyTree(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		link, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(link, dst)
	case info.IsDir():
		if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
			return err
		}
		ents, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range ents {
			if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return nil
	default:
		return copyFile(src, dst, info.Mode().Perm(), info.ModTime())
	}
}

// copyFile copies a single regular file, preserving perm and mtime (copy2-like).
func copyFile(src, dst string, perm os.FileMode, mtime time.Time) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chtimes(dst, mtime, mtime)
}

// originSignature fingerprints an origin tree cheaply (lstat only, no content
// reads): a sha256 over every entry's relpath plus its mode, and for regular
// files the size and mtime, for symlinks the target — in WalkDir's
// deterministic sorted order. copyEntry preserves modes and mtimes, so a
// pull/push round-trip leaves the signature stable, while any realistic host
// edit (content, adds, deletes, renames) changes it. A pathological
// mtime-preserving edit escapes detection — push --force is the escape hatch.
// Directories contribute only relpath+mode: their size/mtime are fs noise, and
// membership changes already show up as child entries.
func originSignature(root string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		fi, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			link, lerr := os.Readlink(p)
			if lerr != nil {
				return lerr
			}
			fmt.Fprintf(h, "%s\x00%v\x00%s\n", rel, fi.Mode(), link)
		case fi.IsDir():
			fmt.Fprintf(h, "%s\x00%v\n", rel, fi.Mode())
		default:
			fmt.Fprintf(h, "%s\x00%v\x00%d\x00%d\n", rel, fi.Mode(), fi.Size(), fi.ModTime().UnixNano())
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// readManifest loads a manifest. A missing file is not an error (it reads as an
// empty manifest), but a present-but-corrupt file is reported so a push can't
// silently no-op and claim success.
func readManifest(file string) ([]ManifestEntry, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var m []ManifestEntry
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest %s is corrupt: %w", file, err)
	}
	return m, nil
}

// writeManifest writes the manifest as indented JSON — in place via
// os.WriteFile, NEVER an atomic rename: in-container the manifest is a
// single-file bind mount, and replacing the inode would detach it from the
// host file.
func writeManifest(file string, m []ManifestEntry) error {
	if m == nil {
		m = []ManifestEntry{}
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(file, data, 0o644)
}

// CopyIn pulls a profile's deps (into workspace/) and context files (to the
// sandbox root) and writes a manifest. A target that already exists is KEPT
// untouched unless Force is set, in which case it is overwritten. Each copied rw origin's signature is recorded in the
// manifest (a kept target carries its previous signature forward), so a later
// push can detect host edits. Progress is reported to w.
func CopyIn(w io.Writer, o PullOpts) error {
	data, err := os.ReadFile(o.ProfileFile)
	if err != nil {
		return err
	}
	var profile Profile
	if err := json.Unmarshal(data, &profile); err != nil {
		return err
	}

	targets := resolveDeps(profile, o.SandboxDir, w, o.InContainer)
	targets = append(targets, resolveContext(profile, o.ProjectRoot, o.SandboxDir, w)...)

	// Previous signatures, carried forward for KEPT targets: a keep leaves the
	// sandbox copy at its old sync point, so blessing the CURRENT origin state
	// would let push silently overwrite host edits made before this pull.
	prevSig := map[string]string{}
	if prev, err := readManifest(o.ManifestFile); err == nil {
		ws := filepath.Join(o.SandboxDir, WorkspaceDir)
		oldLayout := false
		for _, e := range prev {
			prevSig[e.Origin+"\x00"+e.SandboxPath] = e.OriginSig
			oldLayout = oldLayout || (e.Mode == "rw" && !within(ws, e.SandboxPath))
		}
		// A pre-workspace manifest vendored deps at the sandbox root; those
		// stale copies are not cleaned up here — point at recreate once.
		if oldLayout {
			fmt.Fprintf(w, "  NOTE  deps now land under %s/ — this sandbox still has the old flat layout; run `sandboxer recreate` to rebuild it cleanly\n", WorkspaceDir)
		}
	}

	manifest := []ManifestEntry{}
	pulled, kept := 0, 0
	for _, t := range targets {
		entry := ManifestEntry{Mode: t.Mode, Origin: t.Origin, SandboxPath: t.Dest}
		rel, rerr := filepath.Rel(o.SandboxDir, t.Dest)
		if rerr != nil {
			rel = t.Dest
		}
		// In-container the matched "origin" can be the sandbox copy itself (the
		// sandbox dir is the cwd and thus a search root): copying it onto itself
		// would destroy it (copyEntry removes dst first), so it is always KEPT —
		// --force included.
		if t.Origin == t.Dest {
			fmt.Fprintf(w, "  KEEP  %s — origin is the sandbox copy itself\n", rel)
			entry.OriginSig = prevSig[t.Origin+"\x00"+t.Dest]
			manifest = append(manifest, entry)
			kept++
			continue
		}
		if exists(t.Dest) && !o.Force {
			fmt.Fprintf(w, "  KEEP  %s — already exists (use --force to overwrite)\n", rel)
			entry.OriginSig = prevSig[t.Origin+"\x00"+t.Dest]
			manifest = append(manifest, entry)
			kept++
			continue
		}
		if err := copyEntry(t.Origin, t.Dest); err != nil {
			return err
		}
		if t.Mode == "rw" {
			// Best-effort: an unreadable origin leaves the signature empty, which
			// push treats as "changed" — the safe direction.
			if sig, serr := originSignature(t.Origin); serr == nil {
				entry.OriginSig = sig
			}
		}
		manifest = append(manifest, entry)
		pulled++
	}

	if err := writeManifest(o.ManifestFile, manifest); err != nil {
		return err
	}

	rw := 0
	for _, x := range manifest {
		if x.Mode == "rw" {
			rw++
		}
	}
	fmt.Fprintf(w, "pull: %d copied, %d kept; manifest %d (%d rw / %d ro)\n",
		pulled, kept, len(manifest), rw, len(manifest)-rw)
	return nil
}

// CopyOut pushes the rw entries of a manifest from the sandbox back over their
// origins. By default an origin whose signature no longer matches the one
// recorded at pull time (or that has none) is SKIPped — a host edit made after
// the pull is never silently overwritten; Force restores the old wholesale
// overwrite. After a real push each pushed entry's signature is refreshed in
// the manifest. An entry whose sandbox copy is missing is skipped. When DryRun
// is set, the changes are only reported — no files are touched. Progress is
// reported to w.
func CopyOut(w io.Writer, manifestFile string, o PushOpts) error {
	manifest, err := readManifest(manifestFile)
	if err != nil {
		return err
	}
	action := "PUSH"
	if o.DryRun {
		action = "WOULD-PUSH"
	}
	back, missing, changed := 0, 0, 0
	for i := range manifest {
		e := &manifest[i]
		if e.Mode != "rw" {
			continue
		}
		if !exists(e.SandboxPath) {
			fmt.Fprintf(w, "  SKIP  %s — local copy missing\n", e.SandboxPath)
			missing++
			continue
		}
		if !o.Force {
			// A signature error (origin unreadable/deleted) reads as "changed":
			// skipping is the safe direction, and --force still pushes.
			cur, serr := originSignature(e.Origin)
			if serr != nil || e.OriginSig == "" || cur != e.OriginSig {
				fmt.Fprintf(w, "  SKIP  %s — changed on the host since pull (push --force overwrites)\n", e.Origin)
				changed++
				continue
			}
		}
		fmt.Fprintf(w, "  %s %s -> %s\n", action, e.SandboxPath, e.Origin)
		if !o.DryRun {
			if err := copyEntry(e.SandboxPath, e.Origin); err != nil {
				return err
			}
			// The freshly written origin is the new sync point.
			if sig, serr := originSignature(e.Origin); serr == nil {
				e.OriginSig = sig
			}
		}
		back++
	}
	if !o.DryRun && back > 0 {
		// In-place rewrite (writeManifest uses os.WriteFile, never a rename):
		// in-container the manifest is a single-file bind mount, and a rename
		// would detach it from the host file.
		if err := writeManifest(manifestFile, manifest); err != nil {
			return err
		}
	}

	tail := ""
	if missing > 0 {
		tail += fmt.Sprintf(" (%d missing)", missing)
	}
	if changed > 0 {
		tail += fmt.Sprintf(" (%d skipped: changed on the host)", changed)
	}
	summary := fmt.Sprintf("push: %d rw entries restored%s", back, tail)
	if o.DryRun {
		summary = fmt.Sprintf("push (dry-run): %d rw entries would be restored%s", back, tail)
	}
	fmt.Fprintln(w, summary)
	return nil
}

// ---- small helpers --------------------------------------------------------

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// within reports whether target resolves inside base, rejecting absolute paths
// that escape it and any ../ traversal.
func within(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func cwd() string {
	d, err := os.Getwd()
	if err != nil {
		return "."
	}
	return d
}

// mustAbs resolves p to an absolute path, falling back to p on error.
func mustAbs(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

// absJoin joins base and p (p may be absolute, in which case it wins) and
// returns an absolute path.
func absJoin(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return mustAbs(filepath.Join(base, p))
}
