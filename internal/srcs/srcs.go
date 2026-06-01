// Package srcs implements dependency vendoring between a project's origin
// directories/files and a sandbox copy: it pulls "srcs" (origins) into the
// sandbox (CopyIn) and pushes read-write entries back out (CopyOut).
//
// The copy semantics mirror the depsync reference script:
//   - pull KEEPs a target that already exists (unless --force);
//   - push always overwrites the origin (no signature/skip protection);
//   - copy replaces the destination wholesale and preserves symlinks, file
//     modes and mtimes.
//
// `srcs` entries come in two shapes:
//
//	EXPLICIT: {from: "/abs/dir|file", to: "vendor/x", mode: "rw"|"ro"}
//	          copies from -> <sandbox>/<to>; to defaults to basename(from).
//	MATCHER:  {root: "/abs|rel", name|glob|regex: "...", to: ".", mode, depth}
//	          searches under root and copies matches to <sandbox>/<to>/<rel>.
package srcs

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Src is one entry in a profile's srcs list. It is either explicit (From set)
// or a matcher (Name/Glob/Regex set).
type Src struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Mode  string `json:"mode"`
	Root  string `json:"root"`
	Name  string `json:"name"`
	Glob  string `json:"glob"`
	Regex string `json:"regex"`
	Depth int    `json:"depth"`
}

// Profile is the resolved sandbox configuration. It carries either explicit
// srcs entries or a depsync-style roots+deps pair (search deps under roots by
// path suffix), or both.
type Profile struct {
	MainSrc string   `json:"mainSrc"`
	Srcs    []Src    `json:"srcs"`
	Roots   []string `json:"roots"`
	Deps    []string `json:"deps"`
}

// ManifestEntry records one copied target so a later push can find its origin.
// Like depsync's manifest, it is just the mapping (plus the mode); no
// signatures are kept.
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

// skip holds directory/file names never walked into or matched.
var skip = map[string]bool{
	".git":         true,
	"node_modules": true,
	".sandboxer":   true,
}

// globToRe converts an ant-style glob to an anchored regexp:
//   - "**" matches any chars (including "/"); a following "/" is swallowed.
//   - "*" matches any chars except "/".
//   - "?" matches a single non-"/" char.
//   - regexp metacharacters are escaped, other chars are literal.
func globToRe(glob string) *regexp.Regexp {
	var re strings.Builder
	re.WriteByte('^')
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch {
		case c == '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				re.WriteString(".*")
				i++
				if i+1 < len(glob) && glob[i+1] == '/' {
					i++
				}
			} else {
				re.WriteString("[^/]*")
			}
		case c == '?':
			re.WriteString("[^/]")
		case strings.IndexByte(".+^${}()|[]\\", c) >= 0:
			re.WriteByte('\\')
			re.WriteByte(c)
		default:
			re.WriteByte(c)
		}
	}
	re.WriteByte('$')
	return regexp.MustCompile(re.String())
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

// matchEntry is a single matcher hit, carrying enough to build the dest later.
type matchEntry struct {
	Origin string
	Rel    string
	To     string
	Mode   string
}

// matchEntries walks under (defaultRoot/s.Root) and returns matches for the
// configured name/glob/regex test.
func matchEntries(s Src, defaultRoot string) ([]matchEntry, error) {
	rootArg := s.Root
	if rootArg == "" {
		rootArg = "."
	}
	root := absJoin(defaultRoot, rootArg)
	to := s.To
	if to == "" {
		to = "."
	}
	mode := strings.ToLower(orDefault(s.Mode, "rw"))
	depth := s.Depth

	var test func(rel, base string) bool
	switch {
	case s.Name != "":
		re := globToRe(s.Name)
		test = func(_, base string) bool { return re.MatchString(base) }
	case s.Glob != "":
		re := globToRe(s.Glob)
		test = func(rel, _ string) bool { return re.MatchString(rel) }
	case s.Regex != "":
		re, err := regexp.Compile(s.Regex)
		if err != nil {
			return nil, err
		}
		test = func(rel, _ string) bool { return re.MatchString(rel) }
	default:
		return nil, fmt.Errorf("srcs entry without from/name/glob/regex (root=%s)", s.Root)
	}

	var results []matchEntry
	walk(root, depth, func(p string) {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			rel = p
		}
		rel = filepath.ToSlash(rel)
		if test(rel, filepath.Base(p)) {
			results = append(results, matchEntry{Origin: p, Rel: rel, To: to, Mode: mode})
		}
	})
	return results, nil
}

// resolveTargets flattens a profile's srcs into copy jobs rooted at sandboxDir.
func resolveTargets(p Profile, sandboxDir string) ([]target, error) {
	var defaultRoot string
	if p.MainSrc != "" {
		defaultRoot = mustAbs(p.MainSrc)
	} else {
		defaultRoot = cwd()
	}

	var targets []target
	for _, s := range p.Srcs {
		switch {
		case s.From != "":
			origin := mustAbs(s.From)
			to := s.To
			if to == "" {
				to = filepath.Base(origin)
			}
			targets = append(targets, target{
				Origin: origin,
				Dest:   absJoin(sandboxDir, to),
				Mode:   strings.ToLower(orDefault(s.Mode, "rw")),
			})
		case s.Name != "" || s.Glob != "" || s.Regex != "":
			ms, err := matchEntries(s, defaultRoot)
			if err != nil {
				return nil, err
			}
			for _, r := range ms {
				targets = append(targets, target{
					Origin: r.Origin,
					Dest:   absJoin(sandboxDir, filepath.Join(r.To, r.Rel)),
					Mode:   r.Mode,
				})
			}
		default:
			return nil, fmt.Errorf("srcs entry without from/name/glob/regex")
		}
	}
	return targets, nil
}

// resolveDeps turns the depsync-style roots+deps config into copy jobs: each
// dep is searched (by path suffix) under every root, and the first match is
// copied to <sandbox>/<dep>. Not-found and multi-match are reported to w.
func resolveDeps(p Profile, sandboxDir string, w io.Writer) []target {
	if len(p.Deps) == 0 {
		return nil
	}
	roots := p.Roots
	if len(roots) == 0 {
		if p.MainSrc != "" {
			roots = []string{p.MainSrc}
		} else {
			roots = []string{cwd()}
		}
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

// CopyIn pulls a profile's srcs (origins) into sandboxDir and writes a manifest
// to manifestOut. A target that already exists is KEPT untouched unless force
// is set, in which case it is overwritten. Progress is reported to w.
func CopyIn(w io.Writer, profileFile, sandboxDir, manifestOut string, force bool) error {
	data, err := os.ReadFile(profileFile)
	if err != nil {
		return err
	}
	var profile Profile
	if err := json.Unmarshal(data, &profile); err != nil {
		return err
	}

	targets, err := resolveTargets(profile, sandboxDir)
	if err != nil {
		return err
	}
	// depsync-style roots+deps resolve to additional copy jobs.
	targets = append(targets, resolveDeps(profile, sandboxDir, w)...)

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

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
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

// absJoin joins base and p (p may be absolute, in which case it wins, matching
// path.resolve semantics) and returns an absolute path.
func absJoin(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return mustAbs(filepath.Join(base, p))
}
