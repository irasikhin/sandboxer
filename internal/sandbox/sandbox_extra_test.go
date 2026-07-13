package sandbox

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
)

// TestMakeSandboxWithProfile: a profile with roots+deps pulls exactly the deps
// (found by suffix under roots) into the sandbox and writes a manifest.
func TestMakeSandboxWithProfile(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "main.txt"), "main\n") // not a dep → not copied
	depRoot := t.TempDir()
	writeFile(t, filepath.Join(depRoot, "lib", "dep.txt"), "dep\n")

	b, err := ResolveBase(src)
	if err != nil {
		t.Fatal(err)
	}

	prof := config.Profile{Roots: []string{depRoot}, Deps: []string{"lib"}}
	data, _ := json.Marshal(prof)
	if err := b.WriteProfileJSON("feat", data); err != nil {
		t.Fatal(err)
	}

	if err := b.MakeSandbox("feat", io.Discard); err != nil {
		t.Fatalf("MakeSandbox: %v", err)
	}
	dest := b.SandboxDir("feat")
	if _, err := os.Stat(filepath.Join(dest, "workspace", "lib", "dep.txt")); err != nil {
		t.Errorf("dependency not vendored into the sandbox workspace: %v", err)
	}
	// main.txt is not a dep → must not be copied.
	if _, err := os.Stat(filepath.Join(dest, "workspace", "main.txt")); !os.IsNotExist(err) {
		t.Error("non-dep file should not be copied into the sandbox")
	}
	if _, err := os.Stat(b.ManifestPath("feat")); err != nil {
		t.Errorf("manifest not written: %v", err)
	}
}

// TestOpenBaseReadOnly: OpenBase loads existing state without seeding, and is
// a clean (nil, nil) no-op when there is no state — it must never create dirs.
func TestOpenBaseReadOnly(t *testing.T) {
	src := t.TempDir()

	// No state yet → (nil, nil), and nothing is written.
	b, err := OpenBase(src)
	if err != nil {
		t.Fatalf("OpenBase(empty): %v", err)
	}
	if b != nil {
		t.Error("OpenBase should return nil for a non-sandboxer project")
	}
	if _, err := os.Stat(filepath.Join(src, config.StateDirName)); !os.IsNotExist(err) {
		t.Error("OpenBase must not create the .sandboxer dir")
	}

	// After ResolveBase seeds state, OpenBase loads it (Src + Domains).
	seeded, err := ResolveBase(src)
	if err != nil {
		t.Fatal(err)
	}
	got, err := OpenBase(src)
	if err != nil {
		t.Fatalf("OpenBase(seeded): %v", err)
	}
	if got == nil {
		t.Fatal("OpenBase should find seeded state")
	}
	if got.Src != seeded.Src {
		t.Errorf("Src = %q, want %q", got.Src, seeded.Src)
	}
	if got.Domains != config.DefaultDomains {
		t.Errorf("Domains = %q, want defaults", got.Domains)
	}
}
