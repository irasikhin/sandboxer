package sandbox

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/irasikhin/sandboxer/internal/config"
)

// Project is one project sandboxer holds runtime state for, as discovered by a
// sweep of the state root rather than from a cwd. It exists so a listing can be
// HOST-WIDE: sandboxes are meant to run in parallel, and the ones a user forgets
// are exactly the ones in a repo they are not standing in.
type Project struct {
	*Base
	// Gone reports that the recorded project root no longer exists on this
	// host — the checkout was deleted behind sandboxer's back (rm -rf instead
	// of `sandboxer clean`), so its state dir, worktree branches and any
	// session container are leftovers nothing will ever match again. Reported,
	// never auto-removed.
	Gone bool
}

// Projects returns every project with sandboxer state on this host, sorted by
// project root. No new bookkeeping backs this: a state dir is keyed by a hash of
// the project path (config.StateDir), which is one-way, but each dir's
// _meta/run.env already records SRC=<abs project root> from its first use — so
// the state root IS the index. That file's presence is also what tells a project
// dir apart from the project-independent records beside it (machines/, images/).
//
// The result is best-effort by design: no state root, an unreadable root, or a
// dir with no run.env yields fewer entries, never an error — a listing must
// still print what it can find.
func Projects() []Project {
	root := config.StateRoot()
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []Project
	for _, e := range entries {
		// No IsDir check: reading <entry>/_meta/run.env already fails for
		// anything that is not a project state dir, and a dir symlinked into
		// the state root stays usable.
		dir := filepath.Join(root, e.Name())
		env, err := parseEnvFile(filepath.Join(dir, "_meta", "run.env"))
		if err != nil {
			continue
		}
		src := strings.TrimSpace(env["SRC"])
		if src == "" {
			continue
		}
		_, statErr := os.Stat(src)
		out = append(out, Project{
			Base: &Base{Src: src, Dir: dir, Domains: env["DOMAINS"]},
			// Only a definitive absence counts: a stat that fails for any other
			// reason (a permission-denied parent, an unmounted volume) must not
			// report a live project as deleted.
			Gone: os.IsNotExist(statErr),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Src < out[j].Src })
	return out
}
