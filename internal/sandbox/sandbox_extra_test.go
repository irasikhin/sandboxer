package sandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
)

// TestMakeSandboxWithProfile exercises the srcs-vendoring branch of MakeSandbox:
// when a profile.json with srcs is present, dependencies are pulled into the
// copy and a manifest is written.
func TestMakeSandboxWithProfile(t *testing.T) {
	requireExec(t, "rsync", "git")
	isolateGit(t)

	src := t.TempDir()
	writeFile(t, filepath.Join(src, "main.txt"), "main\n")
	dep := filepath.Join(t.TempDir(), "lib")
	writeFile(t, filepath.Join(dep, "dep.txt"), "dep\n")

	b, err := ResolveBase(src)
	if err != nil {
		t.Fatal(err)
	}

	prof := config.Profile{Srcs: []config.Src{{From: dep, To: "vendor", Mode: "rw"}}}
	data, _ := json.Marshal(prof)
	if err := b.WriteProfileJSON("feat", data); err != nil {
		t.Fatal(err)
	}

	if err := b.MakeSandbox("feat", os.Stderr); err != nil {
		t.Fatalf("MakeSandbox: %v", err)
	}
	dest := b.SandboxDir("feat")
	if _, err := os.Stat(filepath.Join(dest, "vendor", "dep.txt")); err != nil {
		t.Errorf("dependency not vendored into sandbox: %v", err)
	}
	if _, err := os.Stat(b.ManifestPath("feat")); err != nil {
		t.Errorf("manifest not written: %v", err)
	}
}

func TestRemoveKeepsOtherCurrent(t *testing.T) {
	isolateGit(t)
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := b.AppendAgent("a"); err != nil {
		t.Fatal(err)
	}
	if err := b.SetCurrent("b"); err != nil { // current is a different slug
		t.Fatal(err)
	}
	if err := b.Remove("a"); err != nil {
		t.Fatal(err)
	}
	if b.Current() != "b" {
		t.Errorf("removing a non-current sandbox cleared current: %q", b.Current())
	}
}

func TestAgentsIgnoresBlankLines(t *testing.T) {
	isolateGit(t)
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b.AgentsListPath(), []byte("one\n\n  \ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := b.Agents(); len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Errorf("Agents with blank lines = %v, want [one two]", got)
	}
}
