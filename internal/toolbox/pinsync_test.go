package toolbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestEmbeddedRevsMatchFlakeLock is the pin-sync guard: the rev pins in the
// embedded toolbox flake (assets/flake.nix) must equal the locked revs in the
// repo-root flake.lock, so "bump flake.lock but forget the embedded flake"
// fails CI instead of silently shipping a default image built from a stale
// pin.
func TestEmbeddedRevsMatchFlakeLock(t *testing.T) {
	locked := readFlakeLockRevs(t)
	nixpkgs := EmbeddedRevs()
	want, ok := locked["nixpkgs"]
	if !ok || want == "" {
		t.Fatal(`flake.lock has no locked rev for node "nixpkgs"`)
	}
	if nixpkgs != want {
		t.Errorf("nixpkgs pin out of sync: embedded flake.nix has %s, flake.lock has %s — "+
			"update internal/toolbox/assets/flake.nix to match", nixpkgs, want)
	}
}

// readFlakeLockRevs parses the repo-root flake.lock (two levels above this
// package — `go test` runs with the package dir as cwd) and returns node name
// → locked rev.
func readFlakeLockRevs(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "flake.lock"))
	if err != nil {
		t.Fatalf("read repo-root flake.lock: %v", err)
	}
	var lock struct {
		Nodes map[string]struct {
			Locked struct {
				Rev string `json:"rev"`
			} `json:"locked"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatalf("parse flake.lock: %v", err)
	}
	revs := make(map[string]string, len(lock.Nodes))
	for name, node := range lock.Nodes {
		revs[name] = node.Locked.Rev
	}
	return revs
}
