package sandbox

import (
	"os"
	"path"
	"path/filepath"
	"strings"
)

// includeIsPattern reports whether include entry p needs on-disk expansion:
// a whole "**" segment or a path.Match metacharacter in any segment. Literal
// entries never touch the disk at resolve time — their behavior (including
// every error message) predates patterns and must not change.
func includeIsPattern(p string) bool {
	for _, seg := range strings.Split(strings.Trim(p, "/"), "/") {
		if seg == "**" || strings.ContainsAny(seg, `*?[\`) {
			return true
		}
	}
	return false
}

// expandInclude resolves one directory pattern to the existing directories it
// selects under root (the absolute worktree path), in deterministic lexical
// order. Segments match directory NAMES via path.Match; a whole "**" segment
// matches any number of directories, including zero. Only real directories are
// yielded — never files, and never symlinks (ReadDir lstats, so a symlinked
// dir has IsDir()==false; it stays reachable through a literal include, where
// checkViewDirs' real-path belt governs). Directories named ".git" are never
// entered or matched. Descent stops at a match: the matched directory is
// mounted whole, so its subtree is covered by that mount. An empty result
// means the pattern selected nothing; the caller decides the error. A ReadDir
// failure propagates — an unreadable dir must fail the resolve, not silently
// narrow the view.
//
// The pattern runs as a tiny NFA over the segment list: a state is the index
// of the next segment to match, and a directory is selected when the terminal
// state len(segs) is alive. A "**" state survives descent (any depth) and, via
// closure, also admits the next segment at the same depth (zero directories) —
// which is what makes "/**/x" match a top-level /x and "/a/**" select /a
// itself. A "**"-free pattern therefore descends at most len(segs) levels and
// only into matching prefixes: no full-tree walk.
func expandInclude(root, pattern string) ([]string, error) {
	segs := strings.Split(strings.Trim(pattern, "/"), "/")
	// closure adds, for every alive "**" state, its zero-width successor.
	// Ascending order resolves chains ("/**/**/x") in one pass.
	closure := func(states map[int]bool) {
		for i := 0; i < len(segs); i++ {
			if states[i] && segs[i] == "**" {
				states[i+1] = true
			}
		}
	}
	var out []string
	var walk func(dir string, states map[int]bool) error
	walk = func(dir string, states map[int]bool) error {
		if states[len(segs)] {
			out = append(out, dir)
			return nil // prune: the mount covers the whole subtree
		}
		entries, err := os.ReadDir(dir) // sorted by filename → deterministic
		if err != nil {
			return err
		}
		for _, e := range entries {
			if !e.IsDir() || e.Name() == ".git" {
				continue
			}
			next := map[int]bool{}
			for i := range segs {
				if !states[i] {
					continue
				}
				if segs[i] == "**" {
					next[i] = true
				} else if ok, _ := path.Match(segs[i], e.Name()); ok {
					next[i+1] = true
				}
			}
			closure(next)
			if len(next) == 0 {
				continue
			}
			if err := walk(filepath.Join(dir, e.Name()), next); err != nil {
				return err
			}
		}
		return nil
	}
	start := map[int]bool{0: true}
	closure(start)
	if err := walk(root, start); err != nil {
		return nil, err
	}
	return out, nil
}
