package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestMCPCatalogAndResolve covers the MCP catalog and resolution: known names
// map to servers and a sorted unique domain union; unknown names error.
func TestMCPCatalogAndResolve(t *testing.T) {
	if len(MCPNames()) == 0 {
		t.Fatal("mcp catalog is empty")
	}
	servers, domains, err := ResolveMCP([]string{"context7", "fetch"})
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 || servers["context7"].Command != "npx" {
		t.Errorf("servers = %v", servers)
	}
	for i := 1; i < len(domains); i++ {
		if domains[i-1] >= domains[i] {
			t.Errorf("domains not sorted+unique: %v", domains)
		}
	}
	var hasC7 bool
	for _, d := range domains {
		if d == "context7.com" {
			hasC7 = true
		}
	}
	if !hasC7 {
		t.Errorf("expected context7.com in %v", domains)
	}
	if _, _, err := ResolveMCP([]string{"nope"}); err == nil {
		t.Error("unknown MCP server must error")
	}
}

// TestSeedMCP covers config seeding: claude's ~/.claude.json gains mcpServers
// while preserving existing keys; an unsupported agent and an empty set are
// no-ops.
func TestSeedMCP(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"oauthAccount":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	servers, _, _ := ResolveMCP([]string{"context7"})

	seeded, err := SeedMCP("claude", home, servers)
	if err != nil || !seeded {
		t.Fatalf("claude seed: seeded=%v err=%v", seeded, err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	if root["oauthAccount"] != "x" {
		t.Error("existing ~/.claude.json key must be preserved")
	}
	ms, ok := root["mcpServers"].(map[string]any)
	if !ok || ms["context7"] == nil {
		t.Errorf("mcpServers not written: %v", root)
	}

	if seeded, err := SeedMCP("codex", home, servers); err != nil || seeded {
		t.Errorf("unsupported agent must be a no-op: seeded=%v err=%v", seeded, err)
	}
	if seeded, _ := SeedMCP("claude", home, nil); seeded {
		t.Error("empty server set must be a no-op")
	}
}

// TestApplyMCP covers the resolve+seed convenience: empty is a no-op; a claude
// profile folds domains and seeds; an unsupported agent folds domains but does
// not seed.
func TestApplyMCP(t *testing.T) {
	home := t.TempDir()
	if d, s, err := ApplyMCP(nil, "claude", home); err != nil || d != nil || s {
		t.Errorf("empty apply: d=%v s=%v err=%v", d, s, err)
	}
	d, seeded, err := ApplyMCP([]string{"fetch"}, "claude", home)
	if err != nil || !seeded || len(d) == 0 {
		t.Errorf("claude apply: d=%v seeded=%v err=%v", d, seeded, err)
	}
	d2, seeded2, err := ApplyMCP([]string{"fetch"}, "aider", home)
	if err != nil || seeded2 || len(d2) == 0 {
		t.Errorf("aider apply: d=%v seeded=%v err=%v", d2, seeded2, err)
	}
	if _, _, err := ApplyMCP([]string{"nope"}, "claude", home); err == nil {
		t.Error("unknown MCP server must error")
	}
}
