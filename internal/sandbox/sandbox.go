// Package sandbox owns the on-disk state under .sandboxer/: the base metadata
// (run.env), per-sandbox project copies, create-time baselines, dependency
// manifests and logs.
//
// A sandbox is a plain rsync copy of the project — no git is involved. To find
// what an agent changed, the copy is compared against a baseline (file
// signatures recorded at create time); changes are returned by copying the
// modified files back to the source project (see Return).
package sandbox

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/srcs"
)

// Base is a resolved project root and its .sandboxer state.
type Base struct {
	Src     string // absolute project root
	Dir     string // <Src>/.sandboxer
	Domains string
	Model   string
}

// ResolveBase resolves src to an absolute path, ensures the state dirs exist,
// seeds run.env on first use, and loads it.
func ResolveBase(src string) (*Base, error) {
	abs, err := filepath.Abs(src)
	if err != nil {
		return nil, fmt.Errorf("no such directory: %s", src)
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("no such directory: %s", src)
	}
	b := &Base{Src: abs, Dir: filepath.Join(abs, config.StateDirName)}
	if err := os.MkdirAll(b.metaDir(), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(b.logsDir(), 0o755); err != nil {
		return nil, err
	}
	runEnv := filepath.Join(b.metaDir(), "run.env")
	if _, err := os.Stat(runEnv); err != nil {
		d := config.LoadDefaults()
		content := fmt.Sprintf("SRC=%s\nMODEL=%s\nDOMAINS=%s\n", abs, d.Model, d.Domains)
		if err := os.WriteFile(runEnv, []byte(content), 0o644); err != nil {
			return nil, err
		}
		if err := os.WriteFile(b.AgentsListPath(), nil, 0o644); err != nil {
			return nil, err
		}
	}
	env, err := parseEnvFile(runEnv)
	if err != nil {
		return nil, err
	}
	b.Domains = env["DOMAINS"]
	b.Model = env["MODEL"]
	return b, nil
}

func (b *Base) metaDir() string { return filepath.Join(b.Dir, "_meta") }
func (b *Base) logsDir() string { return filepath.Join(b.Dir, "_logs") }

// SandboxDir is the copy directory for a sandbox.
func (b *Base) SandboxDir(slug string) string { return filepath.Join(b.Dir, slug) }

// ProfileJSONPath, ManifestPath, MetaFilePath locate per-sandbox metadata files.
func (b *Base) ProfileJSONPath(slug string) string {
	return filepath.Join(b.metaDir(), slug+".profile.json")
}
func (b *Base) ManifestPath(slug string) string {
	return filepath.Join(b.metaDir(), slug+".manifest.json")
}
func (b *Base) baselinePath(slug string) string {
	return filepath.Join(b.metaDir(), slug+".baseline.json")
}
func (b *Base) MetaFilePath(slug string) string {
	return filepath.Join(b.metaDir(), slug+".meta")
}
func (b *Base) LogPath(slug, ext string) string {
	return filepath.Join(b.logsDir(), slug+"."+ext)
}
func (b *Base) AgentsListPath() string { return filepath.Join(b.metaDir(), "agents.list") }
func (b *Base) currentPath() string    { return filepath.Join(b.metaDir(), "current") }

// RunEnvExists reports whether the base has been initialized (at least one
// sandbox created here).
func RunEnvExists(src string) bool {
	abs, err := filepath.Abs(src)
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(abs, config.StateDirName, "_meta", "run.env"))
	return err == nil
}

// WriteProfileJSON stores the resolved profile for a sandbox.
func (b *Base) WriteProfileJSON(slug string, data []byte) error {
	return os.WriteFile(b.ProfileJSONPath(slug), data, 0o644)
}

// SetDomains rewrites the DOMAINS line in run.env.
func (b *Base) SetDomains(domains string) error {
	runEnv := filepath.Join(b.metaDir(), "run.env")
	env, err := parseEnvFile(runEnv)
	if err != nil {
		return err
	}
	env["DOMAINS"] = domains
	b.Domains = domains
	var sb strings.Builder
	for _, k := range []string{"SRC", "MODEL", "DOMAINS"} {
		fmt.Fprintf(&sb, "%s=%s\n", k, env[k])
	}
	return os.WriteFile(runEnv, []byte(sb.String()), 0o644)
}

// MakeSandbox creates (or refreshes) the copy for slug: rsync the project, pull
// srcs, record the baseline and register the sandbox. Progress is written to w.
func (b *Base) MakeSandbox(slug string, w io.Writer) error {
	dest := b.SandboxDir(slug)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	rsync := exec.Command("rsync", "-a", "--delete",
		"--exclude=/"+config.StateDirName,
		"--exclude=node_modules/",
		"--exclude=.git",
		"--exclude=/sandboxer.tasks",
		b.Src+"/", dest+"/")
	rsync.Stdout = w
	rsync.Stderr = w
	if err := rsync.Run(); err != nil {
		return fmt.Errorf("rsync copy failed: %w", err)
	}
	if pj := b.ProfileJSONPath(slug); fileExists(pj) {
		if err := srcs.CopyIn(w, pj, dest, b.ManifestPath(slug), false); err != nil {
			return fmt.Errorf("srcs pull: %w", err)
		}
	}
	if err := b.recordBaseline(slug); err != nil {
		return err
	}
	return b.AppendAgent(slug)
}

// recordBaseline stores a file→signature map of the fresh copy, used later to
// detect what the agent changed.
func (b *Base) recordBaseline(slug string) error {
	base := map[string]string{}
	walkFiles(b.SandboxDir(slug), func(rel, full string) { base[rel] = fileSig(full) })
	data, err := json.MarshalIndent(base, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(b.baselinePath(slug), data, 0o644)
}

func (b *Base) loadBaseline(slug string) map[string]string {
	m := map[string]string{}
	if data, err := os.ReadFile(b.baselinePath(slug)); err == nil {
		_ = json.Unmarshal(data, &m)
	}
	return m
}

// ChangedFiles counts files in the copy that differ from the create-time
// baseline (i.e. what the agent changed or added).
func (b *Base) ChangedFiles(slug string) int {
	baseline := b.loadBaseline(slug)
	n := 0
	walkFiles(b.SandboxDir(slug), func(rel, full string) {
		if fileSig(full) != baseline[rel] {
			n++
		}
	})
	return n
}

// Return copies files the agent changed (vs the baseline) from the copy back to
// the source project. A file whose source copy changed after create is skipped
// unless force. Progress is written to w.
func (b *Base) Return(slug string, force bool, w io.Writer) error {
	baseline := b.loadBaseline(slug)
	dest := b.SandboxDir(slug)
	returned, skipped := 0, 0
	var walkErr error
	walkFiles(dest, func(rel, full string) {
		if walkErr != nil {
			return
		}
		if fileSig(full) == baseline[rel] {
			return // unchanged by the agent
		}
		srcPath := filepath.Join(b.Src, filepath.FromSlash(rel))
		if !force {
			if baseSig, known := baseline[rel]; known && fileSig(srcPath) != baseSig {
				fmt.Fprintf(w, "  SKIP   %s — changed in the source after create (use --force)\n", rel)
				skipped++
				return
			}
		}
		if err := copyBack(full, srcPath); err != nil {
			walkErr = err
			return
		}
		fmt.Fprintf(w, "  RETURN %s\n", rel)
		returned++
	})
	if walkErr != nil {
		return walkErr
	}
	tail := ""
	if skipped > 0 {
		tail = fmt.Sprintf(" (%d skipped)", skipped)
	}
	fmt.Fprintf(w, "return: %d file(s) copied back%s\n", returned, tail)
	return nil
}

// Diff returns a unified diff of the agent's changes (each changed file in the
// copy against its source counterpart). Requires the `diff` tool.
func (b *Base) Diff(slug string) (string, error) {
	baseline := b.loadBaseline(slug)
	dest := b.SandboxDir(slug)
	var out bytes.Buffer
	walkFiles(dest, func(rel, full string) {
		if fileSig(full) == baseline[rel] {
			return
		}
		from := filepath.Join(b.Src, filepath.FromSlash(rel))
		if !fileExists(from) {
			from = os.DevNull
		}
		// diff exits 1 when files differ (expected); the diff text is still on
		// stdout, so ignore the error.
		o, _ := exec.Command("diff", "-u", from, full).Output()
		out.Write(o)
	})
	return out.String(), nil
}

// Agents lists the registered sandbox slugs (one per line in agents.list).
func (b *Base) Agents() []string {
	data, err := os.ReadFile(b.AgentsListPath())
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// AppendAgent registers slug if not already present.
func (b *Base) AppendAgent(slug string) error {
	for _, a := range b.Agents() {
		if a == slug {
			return nil
		}
	}
	f, err := os.OpenFile(b.AgentsListPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, slug)
	return err
}

// RemoveAgent drops slug from agents.list.
func (b *Base) RemoveAgent(slug string) error {
	agents := b.Agents()
	var kept []string
	for _, a := range agents {
		if a != slug {
			kept = append(kept, a)
		}
	}
	out := strings.Join(kept, "\n")
	if out != "" {
		out += "\n"
	}
	return os.WriteFile(b.AgentsListPath(), []byte(out), 0o644)
}

// Current returns the active sandbox slug, or "".
func (b *Base) Current() string {
	data, err := os.ReadFile(b.currentPath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// SetCurrent sets the active sandbox.
func (b *Base) SetCurrent(slug string) error {
	return os.WriteFile(b.currentPath(), []byte(slug+"\n"), 0o644)
}

// ClearCurrent unsets the active sandbox.
func (b *Base) ClearCurrent() error {
	err := os.Remove(b.currentPath())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Remove deletes all state for a single sandbox.
func (b *Base) Remove(slug string) error {
	paths := []string{
		b.SandboxDir(slug),
		b.MetaFilePath(slug),
		b.baselinePath(slug),
		b.ProfileJSONPath(slug),
		b.ManifestPath(slug),
	}
	for _, p := range paths {
		_ = os.RemoveAll(p)
	}
	// Remove rotating logs (<slug>.json, <slug>.err, …).
	if entries, err := os.ReadDir(b.logsDir()); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), slug+".") {
				_ = os.Remove(filepath.Join(b.logsDir(), e.Name()))
			}
		}
	}
	if err := b.RemoveAgent(slug); err != nil {
		return err
	}
	if b.Current() == slug {
		return b.ClearCurrent()
	}
	return nil
}

// --- helpers ----------------------------------------------------------------

// skipDirNames are directories never copied back, diffed or baselined.
var skipDirNames = map[string]bool{
	config.StateDirName: true,
	"node_modules":      true,
	".git":              true,
}

// walkFiles visits every regular file under root (skipping skipDirNames and the
// root sandboxer.tasks), passing the slash-relative path and the absolute path.
func walkFiles(root string, fn func(rel, full string)) {
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // unreadable entry: skip it, keep walking
		}
		if d.IsDir() {
			if p != root && skipDirNames[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		// filepath.Rel cannot fail here: p is always root or below it.
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if rel == "sandboxer.tasks" {
			return nil
		}
		fn(rel, p)
		return nil
	})
}

// fileSig is a cheap stat-based signature (mtimeMs:size); "" for missing files
// or directories.
func fileSig(path string) string {
	st, err := os.Lstat(path)
	if err != nil || st.IsDir() {
		return ""
	}
	return fmt.Sprintf("%d:%d", st.ModTime().UnixMilli(), st.Size())
}

// copyBack copies src over dst, creating parent dirs and preserving file perms.
func copyBack(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	mode := os.FileMode(0o644)
	if st, err := in.Stat(); err == nil {
		mode = st.Mode().Perm()
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func parseEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	env := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '='); i >= 0 {
			env[line[:i]] = line[i+1:]
		}
	}
	return env, sc.Err()
}
