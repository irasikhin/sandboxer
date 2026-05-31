// Package sandbox owns the on-disk state under .sandboxer/: the base metadata
// (run.env), per-sandbox copies, snapshot branches, manifests and logs. It
// mirrors the layout the original bash implementation used so existing trees
// keep working.
package sandbox

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/gitx"
	"github.com/irasikhin/sandboxer/internal/srcs"
)

// Base is a resolved project root and its .sandboxer state (the bash G_* set).
type Base struct {
	Src     string // absolute project root
	Dir     string // <Src>/.sandboxer
	IsGit   bool
	BaseSHA string
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
		isGit := gitx.IsRepo(abs)
		sha := ""
		if isGit {
			sha = gitx.HeadSHA(abs)
		}
		gitFlag := "0"
		if isGit {
			gitFlag = "1"
		}
		content := fmt.Sprintf("SRC=%s\nMODEL=%s\nDOMAINS=%s\nIS_GIT=%s\nBASE_SHA=%s\n",
			abs, d.Model, d.Domains, gitFlag, sha)
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
	b.IsGit = env["IS_GIT"] == "1"
	b.BaseSHA = env["BASE_SHA"]
	b.Domains = env["DOMAINS"]
	b.Model = env["MODEL"]
	return b, nil
}

func (b *Base) metaDir() string { return filepath.Join(b.Dir, "_meta") }
func (b *Base) logsDir() string { return filepath.Join(b.Dir, "_logs") }

// SandboxDir is the copy directory for a sandbox.
func (b *Base) SandboxDir(slug string) string { return filepath.Join(b.Dir, slug) }

// ProfileJSONPath, ManifestPath, BaseFilePath, MetaFilePath locate per-sandbox
// metadata files.
func (b *Base) ProfileJSONPath(slug string) string {
	return filepath.Join(b.metaDir(), slug+".profile.json")
}
func (b *Base) ManifestPath(slug string) string {
	return filepath.Join(b.metaDir(), slug+".manifest.json")
}
func (b *Base) BaseFilePath(slug string) string {
	return filepath.Join(b.metaDir(), slug+".base")
}
func (b *Base) MetaFilePath(slug string) string {
	return filepath.Join(b.metaDir(), slug+".meta")
}
func (b *Base) LogPath(slug, ext string) string {
	return filepath.Join(b.logsDir(), slug+"."+ext)
}
func (b *Base) AgentsListPath() string { return filepath.Join(b.metaDir(), "agents.list") }
func (b *Base) currentPath() string    { return filepath.Join(b.metaDir(), "current") }
func (b *Base) patchesDir(slug string) string {
	return filepath.Join(b.Dir, "_patches", slug)
}

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

// SetDomains rewrites the DOMAINS line in run.env (create --allow-domains, run).
func (b *Base) SetDomains(domains string) error {
	runEnv := filepath.Join(b.metaDir(), "run.env")
	env, err := parseEnvFile(runEnv)
	if err != nil {
		return err
	}
	env["DOMAINS"] = domains
	b.Domains = domains
	var sb strings.Builder
	for _, k := range []string{"SRC", "MODEL", "DOMAINS", "IS_GIT", "BASE_SHA"} {
		fmt.Fprintf(&sb, "%s=%s\n", k, env[k])
	}
	return os.WriteFile(runEnv, []byte(sb.String()), 0o644)
}

// MakeSandbox creates (or refreshes) the copy for slug: rsync the project,
// pull srcs, init a snapshot branch, and register the sandbox. Progress from
// the srcs pull is written to w.
func (b *Base) MakeSandbox(slug string, w io.Writer) error {
	dest := b.SandboxDir(slug)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	rsync := exec.Command("rsync", "-a", "--delete",
		"--exclude=/"+config.StateDirName,
		"--exclude=node_modules/",
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
	if !b.IsGit {
		if err := gitx.Init(dest); err != nil {
			return err
		}
	}
	_ = gitx.CheckoutBranch(dest, "sandbox/"+slug)
	if err := gitx.Snapshot(dest, "sandboxer: snapshot"); err != nil {
		return err
	}
	if sha := gitx.HeadSHA(dest); sha != "" {
		if err := os.WriteFile(b.BaseFilePath(slug), []byte(sha+"\n"), 0o644); err != nil {
			return err
		}
	}
	return b.AppendAgent(slug)
}

// CommitWork stages and snapshots any changes in the sandbox copy.
func (b *Base) CommitWork(slug string) error {
	return gitx.Snapshot(b.SandboxDir(slug), "sandboxer: "+slug)
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
		b.BaseFilePath(slug),
		b.ProfileJSONPath(slug),
		b.ManifestPath(slug),
		b.patchesDir(slug),
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

// BaseRef returns the recorded snapshot base for slug, falling back to the
// project base SHA (or "HEAD" when fallback is empty).
func (b *Base) BaseRef(slug, fallback string) string {
	if data, err := os.ReadFile(b.BaseFilePath(slug)); err == nil {
		if s := strings.TrimSpace(string(data)); s != "" {
			return s
		}
	}
	if fallback != "" {
		return fallback
	}
	return "HEAD"
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
