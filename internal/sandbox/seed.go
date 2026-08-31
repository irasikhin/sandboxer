package sandbox

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/irasikhin/sandboxer/internal/registry"
	"github.com/irasikhin/sandboxer/internal/style"
)

// SeedHome copies the host's agent configs (the registry's seed paths —
// credentials, settings, global memory, skills) into slug's private home, so
// agents inside the sandbox start already authenticated. It is a COPY, never
// a mount, on purpose: the sandbox must not be able to edit the host's real
// config (a hook added to ~/.claude/settings.json from inside would execute
// on the next HOST run — a sandbox escape), and parallel sandboxes must not
// race on one shared file.
//
// Seeding is a per-FILE merge: a file the sandbox home lacks is added, a file
// it has — an in-sandbox login, logout or edit, whole trees included — is
// NEVER overwritten. Merging (rather than skipping a whole existing dir) is
// what lets hostConfigs reach a sandbox that already ran an agent: its
// ~/.claude exists from the first launch, but the credentials inside are what
// the seed is for. The flip side is spelled out in the docs: a file DELETED
// in the sandbox reappears on the next enter while hostConfigs is on. Each
// file lands staged-then-renamed (no torn copies), and per-path failures warn
// and skip — an unreadable host config must not block the sandbox.
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
			if _, err := os.Lstat(src); err != nil {
				continue // not present on the host — nothing to seed
			}
			n, err := seedMerge(src, dst, sp.Skip)
			if err != nil && w != nil {
				style.Warnf(w, "seed ~/%s: %v (partially skipped)", sp.Path, err)
			}
			if n > 0 && w != nil {
				style.Infof(w, "%s: host ~/%s seeded into the sandbox home (%d new)", name, sp.Path, n)
			}
		}
	}
}

// seedMerge walks the host tree at src and adds everything dst is missing:
// regular files with their modes (staged beside the target and renamed, so an
// interrupted copy never leaves a torn file), symlinks recreated as links
// (never followed), directories created owner-accessible. Existing entries —
// whatever their content — are left untouched; an existing non-dir where the
// host has a dir shadows that whole subtree (the sandbox's version wins).
// skip entries are slash paths relative to src. Returns how many entries were
// added; the first few errors are joined, later ones dropped — enough to
// diagnose without flooding.
func seedMerge(src, dst string, skip []string) (added int, err error) {
	skipSet := make(map[string]bool, len(skip))
	for _, s := range skip {
		skipSet[s] = true
	}
	var errs []error
	fail := func(e error) {
		if len(errs) < 3 {
			errs = append(errs, e)
		}
	}
	walkErr := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			fail(err)
			return fs.SkipDir
		}
		rel, err := filepath.Rel(src, p)
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
			fail(err)
			return nil
		}
		exists := false
		if fi, err := os.Lstat(target); err == nil {
			exists = true
			// The sandbox has a non-dir where the host has a dir (or vice
			// versa): the sandbox's version shadows the whole subtree.
			if d.IsDir() && !fi.IsDir() {
				return fs.SkipDir
			}
		}
		switch {
		case d.IsDir():
			if !exists {
				if err := os.MkdirAll(target, info.Mode().Perm()|0o700); err != nil {
					fail(err)
					return fs.SkipDir
				}
				added++
			}
		case exists:
			// File or symlink already in the sandbox home — never overwritten.
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				fail(err)
				return nil
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				fail(err)
				return nil
			}
			if err := os.Symlink(link, target); err != nil {
				fail(err)
				return nil
			}
			added++
		case info.Mode().IsRegular():
			if err := copyFileStaged(p, target, info.Mode().Perm()); err != nil {
				fail(err)
				return nil
			}
			added++
		}
		return nil
	})
	if walkErr != nil {
		fail(walkErr)
	}
	return added, errors.Join(errs...)
}

// copyFileStaged copies src to dst via a same-dir temp file and a rename, so
// an interrupted seed never leaves a torn file that a later merge would then
// treat as the seeded truth. The destination's parent dirs are created first —
// a seed path may point straight at a nested file (e.g. opencode's
// .local/share/opencode/auth.json), whose parent the merge walk never visits.
func copyFileStaged(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	tmp := dst + ".seedtmp"
	_ = os.Remove(tmp) // leftover of an interrupted seed
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
