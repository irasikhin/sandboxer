// Package sandbox owns the on-disk state under .sandboxer/: the base metadata
// (run.env), per-sandbox directories, dependency manifests and logs.
//
// A sandbox is driven entirely by `srcs`: nothing is copied by default. The
// sandbox directory holds exactly the entries listed in the profile's srcs
// (pulled in via the srcs package); an empty srcs means an empty sandbox.
// Changes to read-write entries are pushed back to their origins. No git is
// involved.
package sandbox

import (
	"bufio"
	"fmt"
	"io"
	"os"
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

// SandboxDir is the directory for a sandbox.
func (b *Base) SandboxDir(slug string) string { return filepath.Join(b.Dir, slug) }

// ProfileJSONPath, ManifestPath, MetaFilePath locate per-sandbox metadata files.
func (b *Base) ProfileJSONPath(slug string) string {
	return filepath.Join(b.metaDir(), slug+".profile.json")
}
func (b *Base) ManifestPath(slug string) string {
	return filepath.Join(b.metaDir(), slug+".manifest.json")
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
	if err := config.ValidateDomains(strings.Split(domains, ",")); err != nil {
		return err
	}
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

// MakeSandbox creates the sandbox directory and pulls its srcs (if the profile
// has any). Nothing is copied unless it is listed in srcs. Progress from the
// srcs pull is written to w.
func (b *Base) MakeSandbox(slug string, w io.Writer) error {
	dest := b.SandboxDir(slug)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	if pj := b.ProfileJSONPath(slug); fileExists(pj) {
		if err := srcs.CopyIn(w, pj, dest, b.ManifestPath(slug), false); err != nil {
			return fmt.Errorf("srcs pull: %w", err)
		}
	}
	return b.AppendAgent(slug)
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
