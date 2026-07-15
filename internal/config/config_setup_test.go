package config

import (
	"strings"
	"testing"
)

// TestProfileSetupDecodes checks the `setup` profile field round-trips
// through the strict decoder and the stored JSON.
func TestProfileSetupDecodes(t *testing.T) {
	p, err := decodeProfileJSON([]byte(`{"name":"web","setup":"npm ci\nnpm run build\n"}`))
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
	empty, err := decodeProfileJSON([]byte(`{"name":"web"}`))
	if err != nil {
		t.Fatal(err)
	}
	if empty.Setup != "" {
		t.Errorf("setup should default empty, got %q", empty.Setup)
	}
}

// TestProfileToolsDecode checks the `tools` list field decodes.
func TestProfileToolsDecode(t *testing.T) {
	p, err := decodeProfileJSON([]byte(`{"name":"web","tools":["node","go"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Tools) != 2 || p.Tools[0] != "node" || p.Tools[1] != "go" {
		t.Errorf("tools = %v", p.Tools)
	}
}
