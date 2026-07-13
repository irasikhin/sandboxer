package sandbox

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
)

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
