package sandbox

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/irasikhin/sandboxer/internal/config"
)

// This file holds the exclusion half of the mount-set model: the negated
// include entries (config.SplitInclude's negatives) and the mask targets they
// carve out of a source's exposure. See docs/view-mounts-design.md for the
// exposure half; the mask is enforced by overmounting an EMPTY read-only host
// directory at the excluded path (the CLI builds that mount, the backend emits
// it), so the content is unreachable and any write fails, while the NAME stays
// visible as an empty directory — unlike an unmounted path, which does not
// exist at all.

// sourceExposure resolves one source to the directories it exposes (absolute
// host paths, in include order) and the mask targets its negations carve out of
// them (absolute host paths, in negation order; Mounts sorts and de-duplicates
// the combined list). An exclusion is resolved with the same machinery and the
// same rules as an exposure (viewDirsDetailed): a literal maps lexically, a
// pattern expands against the worktree on disk, zero matches is a hard error,
// and only directories are ever selected.
//
// Applied to the exposure:
//
//   - a negation that is a descendant of another negation is dropped — the
//     shallow mask already covers its whole subtree;
//   - an exposed directory AT or UNDER a negation is dropped (negation wins;
//     this also keeps a later overmount from poking a window through a mask);
//   - the surviving negations strictly under a kept exposed directory become
//     masks; one outside every exposed directory is IGNORED — it is already
//     invisible, and erroring would make a harmless config fatal;
//   - a source left with no exposure at all is a hard error: an empty view is
//     the failure mode the zero-match rule already refuses (fail closed).
func sourceExposure(s Source) ([]string, []string, error) {
	var exposed []viewDir
	if config.WholeRepo(s.Include) {
		// Whole-repo exposure: the worktree is one window (mounted whole, or
		// ridden as the root mount); a negation can only sit inside it.
		exposed = []viewDir{{pattern: "**", dir: s.Path}}
	} else {
		positives, _ := config.SplitInclude(s.Include)
		var err error
		if exposed, err = viewDirsDetailed(s, positives, false); err != nil {
			return nil, nil, err
		}
	}
	_, negatives := config.SplitInclude(s.Include)
	if len(negatives) == 0 {
		return viewDirPaths(exposed), nil, nil
	}

	negDirs, label, err := resolvedNegations(s, negatives)
	if err != nil {
		return nil, nil, err
	}
	sep := string(filepath.Separator)
	var conflict viewDir
	conflictNeg := ""
	keep := make([]viewDir, 0, len(exposed))
	for _, v := range exposed {
		if n, ok := coveringNegation(negDirs, v.dir, sep); ok {
			if conflictNeg == "" {
				conflict, conflictNeg = v, label[n]
			}
			continue
		}
		keep = append(keep, v)
	}
	if len(keep) == 0 {
		return nil, nil, fmt.Errorf("srcs %s: include %s is excluded by %s (a negation wins over the "+
			"whole directory it covers) — nothing would be exposed; drop one of the two entries",
			filepath.Base(s.RepoRoot), conflict.pattern, conflictNeg)
	}

	mounts := viewDirPaths(keep)
	var masks []string
	for _, n := range negDirs {
		for _, v := range keep {
			if strings.HasPrefix(n, v.dir+sep) {
				masks = append(masks, n)
				break
			}
		}
	}
	return mounts, masks, nil
}

// resolvedNegations resolves a source's negation entries and prunes the
// redundant ones: a negation that is a descendant of another negation is
// dropped (the shallow mask covers its subtree). Returned dirs are deduped,
// absolute and sorted — an ancestor sorts before its descendants, so one pass
// drops the covered ones. label maps each dir back to the entry that named it,
// for diagnostics.
func resolvedNegations(s Source, negatives []string) (dirs []string, label map[string]string, err error) {
	detailed, err := viewDirsDetailed(s, negatives, true)
	if err != nil {
		return nil, nil, err
	}
	label = make(map[string]string, len(detailed))
	all := make([]string, len(detailed))
	for i, v := range detailed {
		all[i] = v.dir
		if _, ok := label[v.dir]; !ok {
			label[v.dir] = v.pattern
		}
	}
	all = sortedUnique(all)
	sep := string(filepath.Separator)
	dirs = make([]string, 0, len(all))
	for _, n := range all {
		covered := false
		for _, k := range dirs {
			if strings.HasPrefix(n, k+sep) {
				covered = true
				break
			}
		}
		if !covered {
			dirs = append(dirs, n)
		}
	}
	return dirs, label, nil
}

// coveringNegation reports the negation that removes dir — the one equal to
// dir or one of its ancestors — so every exposed directory at or under a mask
// is dropped before the masks are computed.
func coveringNegation(negDirs []string, dir, sep string) (string, bool) {
	for _, n := range negDirs {
		if dir == n || strings.HasPrefix(dir, n+sep) {
			return n, true
		}
	}
	return "", false
}

// viewDirPaths flattens resolved view dirs to their host paths.
func viewDirPaths(vs []viewDir) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.dir
	}
	return out
}
