package registry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	_ "embed"
)

// The MCP-server catalog: a friendly name (used in a profile's `mcp:`) to the
// stdio launch spec plus the egress domains the server needs. Embedded here as
// the single source of truth.
//
//go:embed mcp.json
var mcpJSON []byte

// MCPServer is one MCP-server catalog entry: how to launch it (stdio) and the
// egress domains it needs folded into the allowlist.
type MCPServer struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Domains []string          `json:"domains,omitempty"`
}

var mcpCatalog map[string]MCPServer

func init() {
	if err := json.Unmarshal(mcpJSON, &mcpCatalog); err != nil {
		panic("registry: invalid embedded mcp.json: " + err.Error())
	}
}

// MCPNames returns the available MCP-server names, sorted.
func MCPNames() []string {
	names := make([]string, 0, len(mcpCatalog))
	for n := range mcpCatalog {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ResolveMCP maps a profile's MCP names to the selected servers and the sorted,
// unique union of the egress domains they need. An unknown name is an error.
func ResolveMCP(names []string) (map[string]MCPServer, []string, error) {
	servers := make(map[string]MCPServer, len(names))
	seen := map[string]bool{}
	var domains []string
	for _, n := range names {
		s, ok := mcpCatalog[n]
		if !ok {
			return nil, nil, fmt.Errorf("unknown MCP server: %s (have: %v)", n, MCPNames())
		}
		servers[n] = s
		for _, d := range s.Domains {
			if !seen[d] {
				seen[d] = true
				domains = append(domains, d)
			}
		}
	}
	sort.Strings(domains)
	return servers, domains, nil
}

// ApplyMCP resolves a profile's MCP names, seeds the config into the sandbox
// home, and returns the domains to fold into the egress allowlist. An empty name
// list is a no-op. A sandbox is not bound to one agent, so the config is always
// seeded in claude's ~/.claude.json format (what claude reads); it is inert for
// other agents, whose MCP-server domains are still folded into the allowlist so
// they can be configured in-agent. homeDir must already exist.
func ApplyMCP(names []string, homeDir string) (domains []string, err error) {
	if len(names) == 0 {
		return nil, nil
	}
	servers, domains, err := ResolveMCP(names)
	if err != nil {
		return nil, err
	}
	if err := seedClaudeMCP(homeDir, servers); err != nil {
		return nil, err
	}
	return domains, nil
}

// seedClaudeMCP merges the servers into ~/.claude.json's mcpServers, preserving
// any existing keys (e.g. login tokens). The file is 0600 — it can sit beside
// credentials.
func seedClaudeMCP(homeDir string, servers map[string]MCPServer) error {
	path := filepath.Join(homeDir, ".claude.json")
	root := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &root) // best-effort merge; a corrupt file is overwritten
	}
	ms := make(map[string]any, len(servers))
	for name, s := range servers {
		entry := map[string]any{"command": s.Command}
		if len(s.Args) > 0 {
			entry["args"] = s.Args
		}
		if len(s.Env) > 0 {
			entry["env"] = s.Env
		}
		ms[name] = entry
	}
	root["mcpServers"] = ms
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600)
}
