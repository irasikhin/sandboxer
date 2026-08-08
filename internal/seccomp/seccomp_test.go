package seccomp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

// profileDoc is the subset of the profile schema the tests inspect.
type profileDoc struct {
	DefaultAction string `json:"defaultAction"`
	Syscalls      []struct {
		Action   string           `json:"action"`
		Names    []string         `json:"names"`
		Args     []map[string]any `json:"args"`
		ErrnoRet *int             `json:"errnoRet"`
		Includes map[string]any   `json:"includes"`
		Excludes map[string]any   `json:"excludes"`
	} `json:"syscalls"`
}

func mergedDoc(t *testing.T) profileDoc {
	t.Helper()
	data, err := Profile()
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	var doc profileDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("merged profile does not parse: %v", err)
	}
	return doc
}

// TestProfileMerge pins the whole point of the package: the merged profile is
// still a deny-by-default filter, and every nested syscall is allowed exactly
// once, unconditionally — no caps condition (the filter is resolved against
// the OUTER container's cap-drop=ALL), no arg filter (the base's clone entry
// historically masked out CLONE_NEWUSER), no errno.
func TestProfileMerge(t *testing.T) {
	doc := mergedDoc(t)
	if doc.DefaultAction != "SCMP_ACT_ERRNO" {
		t.Errorf("defaultAction = %q — the merge must keep the deny-by-default base", doc.DefaultAction)
	}
	seen := map[string]int{}
	for _, e := range doc.Syscalls {
		for _, n := range e.Names {
			if !slices.Contains(nestedSyscalls, n) {
				continue
			}
			seen[n]++
			if e.Action != "SCMP_ACT_ALLOW" {
				t.Errorf("%s: action %q, want SCMP_ACT_ALLOW", n, e.Action)
			}
			if len(e.Args) != 0 {
				t.Errorf("%s: has an arg filter %v, want unconditional", n, e.Args)
			}
			if e.ErrnoRet != nil {
				t.Errorf("%s: has errnoRet %d, want none", n, *e.ErrnoRet)
			}
			if len(e.Includes) != 0 || len(e.Excludes) != 0 {
				t.Errorf("%s: caps-conditional (inc=%v exc=%v), want unconditional", n, e.Includes, e.Excludes)
			}
		}
	}
	for _, s := range nestedSyscalls {
		if seen[s] != 1 {
			t.Errorf("%s appears %d times in the merged profile, want exactly 1", s, seen[s])
		}
	}
}

// TestProfileKeepsTheBase: stripping must only thin entries, never drop the
// rest of the filter — the bulk of the base's allowed syscalls survive.
func TestProfileKeepsTheBase(t *testing.T) {
	doc := mergedDoc(t)
	total := 0
	for _, e := range doc.Syscalls {
		total += len(e.Names)
	}
	// The vendored base allows ~400 syscalls; a merge that lost them would
	// produce a filter nothing can run under.
	if total < 300 {
		t.Errorf("merged profile names only %d syscalls — the base got lost in the merge", total)
	}
	for _, everyday := range []string{"read", "write", "execve", "openat"} {
		found := false
		for _, e := range doc.Syscalls {
			if slices.Contains(e.Names, everyday) && e.Action == "SCMP_ACT_ALLOW" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("base syscall %q missing from the merged profile", everyday)
		}
	}
}

// TestProfileDeterministic: the file name is a content hash, so two builds of
// the same binary must produce identical bytes.
func TestProfileDeterministic(t *testing.T) {
	a, err := Profile()
	if err != nil {
		t.Fatal(err)
	}
	b, err := build() // bypass the Once: a fresh merge, not the cached bytes
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Error("two merges of the same base differ — the content-addressed name breaks")
	}
}

func TestHashAndFileName(t *testing.T) {
	h, err := Hash()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(h) {
		t.Errorf("Hash = %q, want 12 hex chars", h)
	}
	name, err := FileName()
	if err != nil {
		t.Fatal(err)
	}
	if name != "seccomp-"+h+".json" {
		t.Errorf("FileName = %q, want seccomp-%s.json", name, h)
	}
}

// TestWrite: idempotent, content-correct, and re-write-safe (a second Write
// leaves the existing file alone).
func TestWrite(t *testing.T) {
	dir := t.TempDir()
	path, err := Write(dir)
	if err != nil {
		t.Fatal(err)
	}
	name, _ := FileName()
	if path != filepath.Join(dir, name) {
		t.Errorf("Write path = %q, want %q", path, filepath.Join(dir, name))
	}
	want, _ := Profile()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(want) {
		t.Errorf("written file differs from Profile() (err=%v)", err)
	}
	// No temp litter.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("dir holds %d entries after Write, want 1", len(entries))
	}
	// Second write: same path, file untouched.
	before, _ := os.Stat(path)
	again, err := Write(dir)
	if err != nil || again != path {
		t.Errorf("second Write = (%q, %v), want the same path", again, err)
	}
	after, _ := os.Stat(path)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("second Write rewrote an existing file")
	}
}
