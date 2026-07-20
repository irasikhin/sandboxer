package sandbox

import (
	"reflect"
	"strings"
	"testing"
)

// TestFingerprintIDsMatchesMountFingerprint pins the refactor that split
// MountFingerprint into MountIdentities + FingerprintIDs: the composed result
// must stay byte-identical, because the value goes into RunOpts.MountGen and
// thus the session ConfigHash — a changed fingerprint would read as "stale" on
// every existing session the moment this ships.
func TestFingerprintIDsMatchesMountFingerprint(t *testing.T) {
	dirs := []string{t.TempDir(), t.TempDir()}
	if got, want := MountFingerprint(dirs), FingerprintIDs(MountIdentities(dirs)); got != want {
		t.Errorf("MountFingerprint = %q, FingerprintIDs(MountIdentities) = %q", got, want)
	}
	// Empty in → empty out is load-bearing on both halves: it keeps the argv
	// (and hash) of a sandbox with no individual mounts unchanged.
	if got := MountFingerprint(nil); got != "" {
		t.Errorf("MountFingerprint(nil) = %q, want \"\"", got)
	}
	if got := FingerprintIDs(nil); got != "" {
		t.Errorf("FingerprintIDs(nil) = %q, want \"\"", got)
	}
	if got := MountIdentities(nil); got != nil {
		t.Errorf("MountIdentities(nil) = %v, want nil", got)
	}
}

// TestEncodeDecodeMountIDsRoundTrip covers the shapes that would break a naive
// encoding: a path with a SPACE (which would shift InspectSession's
// single-space field split) and one with '=' and '+' (which base64url's
// unpadded alphabet must not emit).
func TestEncodeDecodeMountIDsRoundTrip(t *testing.T) {
	ids := []MountID{
		{Path: "/p/a dir/with spaces", ID: "8:1:42:1.2"},
		{Path: "/p/b+c=d", ID: "missing"},
		{Path: "/p/z", ID: "8:1:43"},
	}
	enc := EncodeMountIDs(ids)
	if enc == "" {
		t.Fatal("EncodeMountIDs returned empty for a small set")
	}
	if strings.ContainsAny(enc, " +=/") {
		t.Errorf("encoded value %q contains a character that breaks the inspect parse or the label", enc)
	}
	if got := DecodeMountIDs(enc); !reflect.DeepEqual(got, ids) {
		t.Errorf("round trip = %+v, want %+v", got, ids)
	}
}

// TestDecodeMountIDsTolerance: the value comes from a container label written
// by a possibly older, possibly foreign sandboxer. Every malformed shape means
// "no baseline", never a crash and never a partial list a diff could
// misinterpret.
func TestDecodeMountIDsTolerance(t *testing.T) {
	for _, s := range []string{"", "!!!not base64!!!", "QQ"} { // "QQ" decodes to "A": one field, odd
		if got := DecodeMountIDs(s); got != nil {
			t.Errorf("DecodeMountIDs(%q) = %+v, want nil", s, got)
		}
	}
}

// TestEncodeMountIDsSizeCap: over the cap the value is dropped whole, not
// truncated. A truncated list would decode into a plausible but WRONG diff —
// every dropped mount reported as removed — while an empty one degrades to the
// honest "no baseline" fallback.
func TestEncodeMountIDsSizeCap(t *testing.T) {
	var huge []MountID
	for i := 0; i < 4000; i++ {
		huge = append(huge, MountID{Path: "/p/" + strings.Repeat("x", 40), ID: "8:1:1"})
	}
	if got := EncodeMountIDs(huge); got != "" {
		t.Errorf("over-cap EncodeMountIDs returned %d chars, want \"\"", len(got))
	}
}

func TestDiffMounts(t *testing.T) {
	for name, tc := range map[string]struct {
		recorded, current []MountID
		want              []MountChange
	}{
		"unchanged": {
			recorded: []MountID{{Path: "/a", ID: "1"}},
			current:  []MountID{{Path: "/a", ID: "1"}},
		},
		"added": {
			recorded: []MountID{{Path: "/a", ID: "1"}},
			current:  []MountID{{Path: "/a", ID: "1"}, {Path: "/b", ID: "2"}},
			want:     []MountChange{{Path: "/b", Kind: MountAdded}},
		},
		"recreated": {
			recorded: []MountID{{Path: "/a", ID: "1"}},
			current:  []MountID{{Path: "/a", ID: "2"}},
			want:     []MountChange{{Path: "/a", Kind: MountRecreated}},
		},
		"removed": {
			recorded: []MountID{{Path: "/a", ID: "1"}, {Path: "/b", ID: "2"}},
			current:  []MountID{{Path: "/b", ID: "2"}},
			want:     []MountChange{{Path: "/a", Kind: MountRemoved}},
		},
		"sorted by path across kinds": {
			recorded: []MountID{{Path: "/b", ID: "1"}, {Path: "/c", ID: "1"}},
			current:  []MountID{{Path: "/a", ID: "9"}, {Path: "/b", ID: "2"}},
			want: []MountChange{
				{Path: "/a", Kind: MountAdded},
				{Path: "/b", Kind: MountRecreated},
				{Path: "/c", Kind: MountRemoved},
			},
		},
		// No baseline yields NO changes: a session created before the label
		// existed must degrade to the old honest answer, not report every
		// mount it has as brand new.
		"no baseline": {
			current: []MountID{{Path: "/a", ID: "1"}},
		},
	} {
		if got := DiffMounts(tc.recorded, tc.current); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: DiffMounts = %+v, want %+v", name, got, tc.want)
		}
	}
}

func TestDescribeMountChanges(t *testing.T) {
	if got := DescribeMountChanges(nil); got != "" {
		t.Errorf("DescribeMountChanges(nil) = %q, want \"\"", got)
	}
	got := DescribeMountChanges([]MountChange{
		{Path: "/a", Kind: MountAdded},
		{Path: "/b", Kind: MountRecreated},
		{Path: "/c", Kind: MountRemoved},
	})
	want := "+ /a (now matches include); ~ /b (recreated on the host); - /c (gone)"
	if got != want {
		t.Errorf("DescribeMountChanges =\n%q\nwant\n%q", got, want)
	}
}

func TestMountPaths(t *testing.T) {
	if got := MountPaths(nil); got != nil {
		t.Errorf("MountPaths(nil) = %v, want nil", got)
	}
	got := MountPaths([]MountID{{Path: "/a", ID: "1"}, {Path: "/b", ID: "2"}})
	if !reflect.DeepEqual(got, []string{"/a", "/b"}) {
		t.Errorf("MountPaths = %v, want [/a /b]", got)
	}
}
