package toolbox

import (
	"strings"
	"testing"
)

// TestFlakeDeclaresLLMAgentsCache guards that the embedded toolbox flake keeps
// its own nixConfig pointing at llm-agents' binary cache. nix only honors the
// nixConfig of the flake it builds (an input's nixConfig is ignored), so this
// MUST be restated here — without it --accept-flake-config applies nothing,
// every agent compiles from source, and gemini-cli's large npm-deps fetch
// OOM-kills the builder (signal 9). Keep in sync with llm-agents.nix.
func TestFlakeDeclaresLLMAgentsCache(t *testing.T) {
	data, err := assets.ReadFile("assets/flake.nix")
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{
		"nixConfig",
		"https://cache.numtide.com",
		"extra-trusted-public-keys",
		"niks3.numtide.com-1:DTx8wZduET09hRmMtKdQDxNNthLQETkc/yaX7M4qK0g=",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("embedded flake.nix missing %q — llm-agents binary cache not wired; "+
				"agents will compile from source and the npm-deps fetch can OOM the builder", want)
		}
	}
}
