// Package srcs implements depsync-style dependency vendoring between a set of
// search roots and a sandbox directory: it locates each dep by path suffix
// under the roots and pulls it into the sandbox (CopyIn), then pushes the
// read-write copies back over their origins (CopyOut).
//
// The copy semantics mirror the depsync reference script:
//   - pull KEEPs a target that already exists (unless --force);
//   - push always overwrites the origin (no signature/skip protection);
//   - copy replaces the destination wholesale and preserves symlinks, file
//     modes and mtimes.
package srcs

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Profile is the resolved sandbox configuration: search roots and the deps to
// find under them.
type Profile struct {
	Roots []string `json:"roots"`
	Deps  []string `json:"deps"`
}

// ManifestEntry records one copied target so a later push can find its origin
// (like depsync's manifest; no signatures are kept).
type ManifestEntry struct {
	Mode        string `json:"mode"`
	Origin      string `json:"origin"`
	SandboxPath string `json:"sandboxPath"`
}

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
// (by path suffix) under every root, and the first match is copied to
// <sandbox>/<dep>. Not-found and multi-match are reported to w.
func resolveDeps(p Profile, sandboxDir string, w io.Writer) []target {
	if len(p.Deps) == 0 {
		return nil
	}
	roots := p.Roots
	if len(roots) == 0 {
		roots = []string{cwd()}
	}
	var out []target
	for _, dep := range p.Deps {
		matches := searchDep(roots, dep)
		switch {
		case len(matches) == 0:
			fmt.Fprintf(w, "  SKIP  %s — not found under roots\n", dep)
			continue
		case len(matches) > 1:
			fmt.Fprintf(w, "  WARN  %s — %d matches, using %s\n", dep, len(matches), matches[0])
		}
		out = append(out, target{
			Origin: matches[0],
			Dest:   absJoin(sandboxDir, dep),
			Mode:   "rw",
		})
	}
	return out
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

// readManifest loads a manifest, tolerating a missing or garbage file (returns
// an empty slice).
func readManifest(file string) []ManifestEntry {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var m []ManifestEntry
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	return m
}

// writeManifest writes the manifest as indented JSON.
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

// CopyIn pulls a profile's deps into sandboxDir and writes a manifest to
// manifestOut. A target that already exists is KEPT untouched unless force is
// set, in which case it is overwritten. Progress is reported to w.
func CopyIn(w io.Writer, profileFile, sandboxDir, manifestOut string, force bool) error {
	data, err := os.ReadFile(profileFile)
	if err != nil {
		return err
	}
	var profile Profile
	if err := json.Unmarshal(data, &profile); err != nil {
		return err
	}

	targets := resolveDeps(profile, sandboxDir, w)

	manifest := []ManifestEntry{}
	pulled, kept := 0, 0
	for _, t := range targets {
		entry := ManifestEntry{Mode: t.Mode, Origin: t.Origin, SandboxPath: t.Dest}
		if exists(t.Dest) && !force {
			rel, err := filepath.Rel(sandboxDir, t.Dest)
			if err != nil {
				rel = t.Dest
			}
			fmt.Fprintf(w, "  KEEP  %s — already exists (use --force to overwrite)\n", rel)
			manifest = append(manifest, entry)
			kept++
			continue
		}
		if err := copyEntry(t.Origin, t.Dest); err != nil {
			return err
		}
		manifest = append(manifest, entry)
		pulled++
	}

	if err := writeManifest(manifestOut, manifest); err != nil {
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
// origins, always overwriting (like depsync's push). An entry whose sandbox
// copy is missing is skipped. Progress is reported to w.
func CopyOut(w io.Writer, manifestFile string) error {
	manifest := readManifest(manifestFile)
	back, missing := 0, 0
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
		if err := copyEntry(e.SandboxPath, e.Origin); err != nil {
			return err
		}
		fmt.Fprintf(w, "  PUSH  %s -> %s\n", e.SandboxPath, e.Origin)
		back++
	}

	tail := ""
	if missing > 0 {
		tail = fmt.Sprintf(" (%d missing)", missing)
	}
	fmt.Fprintf(w, "push: %d rw entries restored%s\n", back, tail)
	return nil
}

// ---- small helpers --------------------------------------------------------

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
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
