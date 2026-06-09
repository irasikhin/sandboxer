package config

import "testing"

// TestSplitCSVAndFirstNonEmpty covers the two small CSV/selection helpers:
// splitCSV trims and drops empty entries, firstNonEmpty returns the first
// non-empty value (or "").
func TestSplitCSVAndFirstNonEmpty(t *testing.T) {
	if got := splitCSV("  "); got != nil {
		t.Errorf("splitCSV(blank) = %v, want nil", got)
	}
	got := splitCSV("a , ,b,c ")
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("splitCSV = %v, want [a b c]", got)
	}
	if v := firstNonEmpty("", "", "x", "y"); v != "x" {
		t.Errorf("firstNonEmpty = %q, want x", v)
	}
	if v := firstNonEmpty("", ""); v != "" {
		t.Errorf("firstNonEmpty(empties) = %q, want \"\"", v)
	}
}

// TestValidateSkipAndParseErrors pins two branches the other validator tests
// miss: ValidateDomains skipping blank entries (still valid overall) and
// ValidateProxy reporting an unparseable upstream URL.
func TestValidateSkipAndParseErrors(t *testing.T) {
	if err := ValidateDomains([]string{"", "  ", "ok.com"}); err != nil {
		t.Errorf("ValidateDomains(blanks + ok) = %v, want nil", err)
	}
	if err := ValidateProxy(Proxy{Upstream: "http://%zz"}); err == nil {
		t.Error("ValidateProxy(unparseable upstream) = nil, want parse error")
	}
}
