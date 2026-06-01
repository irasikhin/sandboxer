package sandbox

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
)

// TestMakeSandboxWithProfile: a profile with srcs pulls exactly those entries
// into the sandbox and writes a manifest. Nothing else is copied.
func TestMakeSandboxWithProfile(t *testing.T) {
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "main.txt"), "main\n") // NOT in srcs → not copied
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

	if err := b.MakeSandbox("feat", io.Discard); err != nil {
		t.Fatalf("MakeSandbox: %v", err)
	}
	dest := b.SandboxDir("feat")
	if _, err := os.Stat(filepath.Join(dest, "vendor", "dep.txt")); err != nil {
		t.Errorf("dependency not vendored into sandbox: %v", err)
	}
	// main.txt is in the project root but not in srcs → must not be copied.
	if _, err := os.Stat(filepath.Join(dest, "main.txt")); !os.IsNotExist(err) {
		t.Error("non-srcs file should not be copied into the sandbox")
	}
	if _, err := os.Stat(b.ManifestPath("feat")); err != nil {
		t.Errorf("manifest not written: %v", err)
	}
}
