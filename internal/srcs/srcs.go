// Package srcs implements dependency vendoring between a project's origin
// directories/files and a sandbox copy: it pulls "srcs" (origins) into the
// sandbox (CopyIn) and pushes read-write entries back out (CopyOut), tracking
// state in a manifest so that locally/externally modified files are preserved
// unless --force is given.
//
// It is a faithful port of libexec/sandboxer-cfg.mjs (the `in`/`out` commands
// and their helpers). The `eval` command (nix eval) is intentionally not
// ported: configuration is Go-native now.
//
// `srcs` entries come in two shapes:
//
//	EXPLICIT: {from: "/abs/dir|file", to: "vendor/x", mode: "rw"|"ro"}
//	          copies from -> <sandbox>/<to>; to defaults to basename(from).
//	MATCHER:  {root: "/abs|rel", name|glob|regex: "...", to: ".", mode, depth}
//	          searches under root and copies matches to <sandbox>/<to>/<rel>.
package srcs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
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

// Profile is the resolved sandbox configuration: a main source root and a list
// of srcs entries to vendor.
type Profile struct {
	MainSrc string `json:"mainSrc"`
	Srcs    []Src  `json:"srcs"`
}

// ManifestEntry records one copied target so subsequent pulls/pushes can detect
// local/external modifications. Sigs are cheap stat-based signatures; an empty
// sig means "unknown" (the mjs used null).
type ManifestEntry struct {
	Mode        string `json:"mode"`
	Origin      string `json:"origin"`
	SandboxPath string `json:"sandboxPath"`
	OriginSig   string `json:"originSig"`
	DestSig     string `json:"destSig"`
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

// sig returns a cheap stat-based signature for change detection (no content
// reads). Missing path -> "" (unknown). Directory -> "d:"+sha256 over sorted
// "rel:mtimeMs:size" lines for every entry under it. File -> "f:mtimeMs:size".
func sig(path string) string {
	st, err := os.Lstat(path)
	if err != nil {
		return ""
	}
	if st.IsDir() {
		var items []string
		walk(path, 0, func(f string) {
			s, err := os.Lstat(f)
			if err != nil {
				return
			}
			rel, err := filepath.Rel(path, f)
			if err != nil {
				rel = f
			}
			items = append(items, fmt.Sprintf("%s:%d:%d", rel, s.ModTime().UnixMilli(), s.Size()))
		})
		sort.Strings(items)
		h := sha256.Sum256([]byte(strings.Join(items, "\n")))
		return "d:" + hex.EncodeToString(h[:])
	}
	return fmt.Sprintf("f:%d:%d", st.ModTime().UnixMilli(), st.Size())
}

// copyPath recursively copies src to dst, creating parent dirs and overwriting
// existing files (force). Directory trees are recreated with file perms.
func copyPath(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(src, p)
			if err != nil {
				return err
			}
			target := filepath.Join(dst, rel)
			if d.IsDir() {
				di, err := d.Info()
				if err != nil {
					return err
				}
				return os.MkdirAll(target, di.Mode().Perm())
			}
			fi, err := d.Info()
			if err != nil {
				return err
			}
			return copyFile(p, target, fi.Mode().Perm())
		})
	}
	return copyFile(src, dst, info.Mode().Perm())
}

// copyFile copies a single file, overwriting dst, with the given perm.
func copyFile(src, dst string, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
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
	return out.Close()
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
// to manifestOut. Targets that exist in the sandbox and were modified locally
// (per the previous manifest's destSig) are kept untouched unless force is set,
// in which case they are overwritten. Progress is reported to w.
func CopyIn(w io.Writer, profileFile, sandboxDir, manifestOut string, force bool) error {
	data, err := os.ReadFile(profileFile)
	if err != nil {
		return err
	}
	var profile Profile
	if err := json.Unmarshal(data, &profile); err != nil {
		return err
	}

	prev := make(map[string]ManifestEntry)
	for _, e := range readManifest(manifestOut) {
		prev[e.SandboxPath] = e
	}

	targets, err := resolveTargets(profile, sandboxDir)
	if err != nil {
		return err
	}

	manifest := []ManifestEntry{}
	pulled, kept := 0, 0
	for _, t := range targets {
		if exists(t.Dest) && !force {
			if p, ok := prev[t.Dest]; ok && sig(t.Dest) != p.DestSig {
				rel, err := filepath.Rel(sandboxDir, t.Dest)
				if err != nil {
					rel = t.Dest
				}
				fmt.Fprintf(w, "  KEEP  %s — modified locally (use --force to overwrite)\n", rel)
				manifest = append(manifest, p)
				kept++
				continue
			}
		}
		if err := copyPath(t.Origin, t.Dest); err != nil {
			return err
		}
		manifest = append(manifest, ManifestEntry{
			Mode:        t.Mode,
			Origin:      t.Origin,
			SandboxPath: t.Dest,
			OriginSig:   sig(t.Origin),
			DestSig:     sig(t.Dest),
		})
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
// origins. An origin that changed outside the sandbox after the pull (per its
// originSig) is skipped unless force is set. The manifest is rewritten with
// refreshed origin sigs. Progress is reported to w.
func CopyOut(w io.Writer, manifestFile string, force bool) error {
	manifest := readManifest(manifestFile)
	back, missing, skipped := 0, 0, 0
	for i := range manifest {
		e := &manifest[i]
		if e.Mode != "rw" {
			continue
		}
		if !exists(e.SandboxPath) {
			missing++
			continue
		}
		if exists(e.Origin) && !force && e.OriginSig != "" && sig(e.Origin) != e.OriginSig {
			fmt.Fprintf(w, "  SKIP  %s — changed outside the sandbox after pull (use --force to overwrite)\n", e.Origin)
			skipped++
			continue
		}
		if err := copyPath(e.SandboxPath, e.Origin); err != nil {
			return err
		}
		e.OriginSig = sig(e.Origin)
		back++
	}

	if err := writeManifest(manifestFile, manifest); err != nil {
		return err
	}

	var parts []string
	if missing > 0 {
		parts = append(parts, fmt.Sprintf("%d missing", missing))
	}
	if skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", skipped))
	}
	tail := ""
	if len(parts) > 0 {
		tail = " (" + strings.Join(parts, ", ") + ")"
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
