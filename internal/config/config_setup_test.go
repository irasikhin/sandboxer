package config

import (
	"strings"
	"testing"
)

// TestProfileSetupDecodes checks the new `setup:` profile field round-trips
// through the strict YAML decoder and the stored JSON.
func TestProfileSetupDecodes(t *testing.T) {
	p, err := decodeProfile([]byte("name: web\nsetup: |\n  npm ci\n  npm run build\n"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "npm ci\nnpm run build\n"; p.Setup != want {
		t.Errorf("setup = %q, want %q", p.Setup, want)
	}

	j, err := p.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if got := string(j); !strings.Contains(got, `"setup"`) {
		t.Errorf("JSON missing setup field: %s", got)
	}

	// Absent setup stays empty and is omitted from JSON.
	empty, err := decodeProfile([]byte("name: web\n"))
	if err != nil {
		t.Fatal(err)
	}
	if empty.Setup != "" {
		t.Errorf("setup should default empty, got %q", empty.Setup)
	}
}

// TestProfileToolsAndMCPDecode checks the `tools:` and `mcp:` list fields decode
// from YAML.
func TestProfileToolsAndMCPDecode(t *testing.T) {
	p, err := decodeProfile([]byte("name: web\ntools: [node, go]\nmcp: [context7]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Tools) != 2 || p.Tools[0] != "node" || p.Tools[1] != "go" {
		t.Errorf("tools = %v", p.Tools)
	}
	if len(p.MCP) != 1 || p.MCP[0] != "context7" {
		t.Errorf("mcp = %v", p.MCP)
	}
}
