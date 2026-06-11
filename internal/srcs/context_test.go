package srcs

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// TestContextDefaults: with no context: list, the existing default entries
// (CLAUDE.md, .claude) are copied read-only to the sandbox ROOT; the missing
// one (AGENTS.md) is skipped silently, and push never returns any of them.
func TestContextDefaults(t *testing.T) {
	project := t.TempDir()
	writeFile(t, filepath.Join(project, "CLAUDE.md"), "rules\n")
	writeFile(t, filepath.Join(project, ".claude", "skills", "s.md"), "skill\n")

	sandbox := filepath.Join(t.TempDir(), "sb")
	manifest := filepath.Join(t.TempDir(), "m.json")
	pf := writeProfile(t, t.TempDir(), Profile{})

	var out bytes.Buffer
	opts := PullOpts{ProfileFile: pf, SandboxDir: sandbox, ManifestFile: manifest, ProjectRoot: project}
	if err := CopyIn(&out, opts); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(sandbox, "CLAUDE.md")); got != "rules\n" {
		t.Errorf("CLAUDE.md not copied to the sandbox root: %q", got)
	}
	if !exists(filepath.Join(sandbox, ".claude", "skills", "s.md")) {
		t.Error(".claude/ not copied to the sandbox root")
	}
	if exists(filepath.Join(sandbox, "AGENTS.md")) {
		t.Error("a missing default context entry must not be created")
	}
	if strings.Contains(out.String(), "SKIP") {
		t.Errorf("missing DEFAULT context entries must be skipped silently: %q", out.String())
	}
	m, err := readManifest(manifest)
	if err != nil || len(m) != 2 {
		t.Fatalf("manifest = %v entries, err=%v; want 2 (CLAUDE.md, .claude)", len(m), err)
	}
	for _, e := range m {
		if e.Mode != "ro" || e.OriginSig != "" {
			t.Errorf("context entry %s: mode=%q sig=%q, want ro and no sig", e.SandboxPath, e.Mode, e.OriginSig)
		}
	}

	// Even a forced push must never write a context file back to the project.
	writeFile(t, filepath.Join(sandbox, "CLAUDE.md"), "agent-tampered\n")
	out.Reset()
	if err := CopyOut(&out, manifest, PushOpts{Force: true}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(project, "CLAUDE.md")); got != "rules\n" {
		t.Errorf("push wrote a ro context file back: %q", got)
	}
}

// TestContextExplicitOverride: a non-empty context: list REPLACES the default
// set, and a missing explicit entry warns (unlike a missing default).
func TestContextExplicitOverride(t *testing.T) {
	project := t.TempDir()
	writeFile(t, filepath.Join(project, "CLAUDE.md"), "rules\n")
	writeFile(t, filepath.Join(project, "NOTES.md"), "notes\n")

	sandbox := filepath.Join(t.TempDir(), "sb")
	manifest := filepath.Join(t.TempDir(), "m.json")
	pf := writeProfile(t, t.TempDir(), Profile{Context: []string{"NOTES.md", "missing.md"}})

	var out bytes.Buffer
	opts := PullOpts{ProfileFile: pf, SandboxDir: sandbox, ManifestFile: manifest, ProjectRoot: project}
	if err := CopyIn(&out, opts); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(sandbox, "NOTES.md")); got != "notes\n" {
		t.Errorf("explicit context entry not copied: %q", got)
	}
	if exists(filepath.Join(sandbox, "CLAUDE.md")) {
		t.Error("an explicit context: list must replace the default set")
	}
	if !strings.Contains(out.String(), "missing.md — not found") {
		t.Errorf("missing EXPLICIT context entry must warn: %q", out.String())
	}
}

// TestContextRefusesEscapes: context entries may not escape the sandbox root
// or shadow the workspace dir.
func TestContextRefusesEscapes(t *testing.T) {
	project := t.TempDir()
	writeFile(t, filepath.Join(project, "x"), "x\n")

	sandbox := filepath.Join(t.TempDir(), "sb")
	manifest := filepath.Join(t.TempDir(), "m.json")
	pf := writeProfile(t, t.TempDir(), Profile{Context: []string{"../x", "/etc/hosts", WorkspaceDir}})

	var out bytes.Buffer
	opts := PullOpts{ProfileFile: pf, SandboxDir: sandbox, ManifestFile: manifest, ProjectRoot: project}
	if err := CopyIn(&out, opts); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(out.String(), "must stay at the sandbox root"); got != 3 {
		t.Errorf("want 3 refusals, got %d: %q", got, out.String())
	}
	if m, _ := readManifest(manifest); len(m) != 0 {
		t.Errorf("no context entry should have been copied: %+v", m)
	}
}

// TestContextKeepAndForce: a context copy behaves like any pulled target — an
// existing copy is KEPT on re-pull, --force restores it from the origin.
func TestContextKeepAndForce(t *testing.T) {
	project := t.TempDir()
	writeFile(t, filepath.Join(project, "CLAUDE.md"), "rules\n")

	sandbox := filepath.Join(t.TempDir(), "sb")
	manifest := filepath.Join(t.TempDir(), "m.json")
	pf := writeProfile(t, t.TempDir(), Profile{})
	opts := PullOpts{ProfileFile: pf, SandboxDir: sandbox, ManifestFile: manifest, ProjectRoot: project}

	var out bytes.Buffer
	if err := CopyIn(&out, opts); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(sandbox, "CLAUDE.md")
	writeFile(t, dest, "local-tweak\n")
	if err := CopyIn(&out, opts); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, dest); got != "local-tweak\n" {
		t.Errorf("re-pull must KEEP an existing context copy: %q", got)
	}
	opts.Force = true
	if err := CopyIn(&out, opts); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, dest); got != "rules\n" {
		t.Errorf("forced pull must refresh the context copy: %q", got)
	}

	// No project root (e.g. an unrecognized in-container layout) → no context.
	empty := filepath.Join(t.TempDir(), "sb2")
	opts = PullOpts{ProfileFile: pf, SandboxDir: empty, ManifestFile: filepath.Join(t.TempDir(), "m2.json")}
	if err := CopyIn(&out, opts); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(empty, "CLAUDE.md")) {
		t.Error("no ProjectRoot must mean no context entries")
	}
}
