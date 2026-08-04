package toolbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoExtraBinaryCache guards that neither the embedded toolbox flake nor
// the repo's root flake declares an extra binary cache in a nixConfig.
// llm-agents' cache (cache.numtide.com) was removed: it answers but crawls
// from some networks, and nix then stalls on every path (30s+ per
// stalled-download-timeout) before disabling the substituter and falling back
// to cache.nixos.org — agents now compile from source in the builder. Adding a
// cache back is a deliberate decision: make it consciously and update this
// test.
func TestNoExtraBinaryCache(t *testing.T) {
	embedded, err := assets.ReadFile("assets/flake.nix")
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.ReadFile(filepath.Join("..", "..", "flake.nix"))
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"embedded toolbox flake": embedded,
		"root flake":             root,
	} {
		s := string(data)
		for _, banned := range []string{"nixConfig", "cache.numtide.com"} {
			if strings.Contains(s, banned) {
				t.Errorf("%s declares %q — an extra binary cache was deliberately removed; "+
					"re-adding it stalls builds on networks where the cache crawls", name, banned)
			}
		}
	}
}
