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
	if _, err := os.Stat(filepath.Join(dest, "lib", "dep.txt")); err != nil {
		t.Errorf("dependency not vendored into sandbox: %v", err)
	}
	// main.txt is not a dep → must not be copied.
	if _, err := os.Stat(filepath.Join(dest, "main.txt")); !os.IsNotExist(err) {
		t.Error("non-dep file should not be copied into the sandbox")
	}
	if _, err := os.Stat(b.ManifestPath("feat")); err != nil {
		t.Errorf("manifest not written: %v", err)
	}
}
