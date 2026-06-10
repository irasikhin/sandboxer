package registry

import "testing"

// TestToolsCatalogAndResolve covers the tool-pack catalog and resolution:
// known packs map to sorted unique nixpkgs attrs, duplicates collapse, an empty
// input is empty, and an unknown pack is an error.
func TestToolsCatalogAndResolve(t *testing.T) {
	if len(ToolNames()) == 0 {
		t.Fatal("tool catalog is empty")
	}

	attrs, err := ResolveTools([]string{"node", "python"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"nodejs": true, "python3": true, "uv": true}
	if len(attrs) != len(want) {
		t.Fatalf("attrs = %v, want keys %v", attrs, want)
	}
	for _, a := range attrs {
		if !want[a] {
			t.Errorf("unexpected attr %q", a)
		}
	}
	for i := 1; i < len(attrs); i++ {
		if attrs[i-1] > attrs[i] {
			t.Errorf("attrs not sorted: %v", attrs)
		}
	}

	if dd, err := ResolveTools([]string{"go", "go"}); err != nil || len(dd) != 1 || dd[0] != "go" {
		t.Errorf("dedup failed: %v (err %v)", dd, err)
	}
	if e, err := ResolveTools(nil); err != nil || len(e) != 0 {
		t.Errorf("empty resolve: %v (err %v)", e, err)
	}
	if _, err := ResolveTools([]string{"cobol"}); err == nil {
		t.Error("unknown tool pack must error")
	}
}
