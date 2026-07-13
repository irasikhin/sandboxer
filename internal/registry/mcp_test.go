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

// TestSeedClaudeMCP covers config seeding: claude's ~/.claude.json gains
// mcpServers while preserving existing keys.
func TestSeedClaudeMCP(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"oauthAccount":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	servers, _, _ := ResolveMCP([]string{"context7"})

	if err := seedClaudeMCP(home, servers); err != nil {
		t.Fatalf("claude seed: %v", err)
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
}

// TestApplyMCP covers the resolve+seed convenience: empty is a no-op; a profile
// with servers folds their domains and seeds the claude config; an unknown
// server name errors.
func TestApplyMCP(t *testing.T) {
	home := t.TempDir()
	if d, err := ApplyMCP(nil, home); err != nil || d != nil {
		t.Errorf("empty apply: d=%v err=%v", d, err)
	}
	d, err := ApplyMCP([]string{"fetch"}, home)
	if err != nil || len(d) == 0 {
		t.Errorf("apply: d=%v err=%v", d, err)
	}
	// The config is always seeded in claude's format (inert for other agents).
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); err != nil {
		t.Errorf("ApplyMCP should seed ~/.claude.json: %v", err)
	}
	if _, err := ApplyMCP([]string{"nope"}, home); err == nil {
		t.Error("unknown MCP server must error")
	}
}
