package sandbox

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/irasikhin/sandboxer/internal/registry"
)

// SeedHome copies the host's agent configs (the registry's seed paths —
// credentials, settings, global memory) into slug's private home, so agents
// inside the sandbox start already authenticated. It is a COPY, never a
// mount, on purpose: the sandbox must not be able to edit the host's real
// config (a hook added to ~/.claude/settings.json from inside would execute
// on the next HOST run — a sandbox escape), and parallel sandboxes must not
// race on one shared file. Each path is seeded only while ABSENT in the
// sandbox home — an in-sandbox login, logout or edit is never overwritten —
// and a copy is staged then renamed, so an interrupted seed never passes for
// a complete one. Per-path failures warn and skip: an unreadable host config
// must not block the sandbox.
func (b *Base) SeedHome(slug string, w io.Writer) {
	hostHome, err := os.UserHomeDir()
	if err != nil {
		return // no resolvable host home — nothing to seed
	}
	home := b.HomeDir(slug)
	for _, name := range registry.Names() {
		a, err := registry.Get(name)
		if err != nil {
			continue
		}
		for _, sp := range a.Seed {
			src := filepath.Join(hostHome, filepath.FromSlash(sp.Path))
			dst := filepath.Join(home, filepath.FromSlash(sp.Path))
			if _, err := os.Lstat(dst); err == nil {
				continue // already in the sandbox home — never overwrite
			}
			if _, err := os.Lstat(src); err != nil {
				continue // not present on the host — nothing to seed
			}
			if err := seedCopy(src, dst, sp.Skip); err != nil {
				if w != nil {
					fmt.Fprintf(w, "sandboxer: seed ~/%s: %v (skipped)\n", sp.Path, err)
				}
				continue
			}
			if w != nil {
				fmt.Fprintf(w, "sandboxer: %s: host ~/%s seeded into the sandbox home\n", name, sp.Path)
			}
		}
	}
}

// seedCopy copies src (a file, directory or symlink) to dst, skipping the
// given subpaths. The copy is staged beside dst and renamed into place, so a
// torn copy never looks seeded (SeedHome's absence check would otherwise
// accept it forever).
func seedCopy(src, dst string, skip []string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".seedtmp"
	_ = os.RemoveAll(tmp) // leftover of an interrupted seed
	if err := copyTree(src, tmp, skip); err != nil {
		_ = os.RemoveAll(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// copyTree copies the tree at root to dst: regular files with their modes,
// directories owner-accessible, symlinks recreated as-is (never followed —
// a link out of the tree stays a link). skip entries are slash paths relative
// to root; anything else irregular (sockets, fifos) is not config material
// and is left behind.
func copyTree(root, dst string, skip []string) error {
	skipSet := make(map[string]bool, len(skip))
	for _, s := range skip {
		skipSet[s] = true
	}
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		target := dst
		if rel != "." {
			if skipSet[filepath.ToSlash(rel)] {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			target = filepath.Join(dst, rel)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case d.IsDir():
			// Owner bits forced on: the copy must stay writable while it fills.
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode().IsRegular():
			return copyFile(p, target, info.Mode().Perm())
		default:
			return nil
		}
	})
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
