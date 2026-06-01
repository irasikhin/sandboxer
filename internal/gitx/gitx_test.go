package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// isolateGit points git at empty global/system config so the identity-fallback
// behaviour is deterministic regardless of the host's ~/.gitconfig.
func isolateGit(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(t.TempDir(), "global"))
	t.Setenv("GIT_CONFIG_SYSTEM", filepath.Join(t.TempDir(), "system"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// rawGit runs git in dir and fails the test on error.
func rawGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// newRepo returns a fresh, git-isolated repository. With localIdentity set, a
// user.email is configured locally; otherwise the identity-fallback path runs.
func newRepo(t *testing.T, localIdentity bool) string {
	t.Helper()
	isolateGit(t)
	dir := t.TempDir()
	if err := Init(dir); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if localIdentity {
		rawGit(t, dir, "config", "user.email", "dev@example.com")
		rawGit(t, dir, "config", "user.name", "dev")
	}
	return dir
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIsRepo(t *testing.T) {
	isolateGit(t)
	bare := t.TempDir()
	if IsRepo(bare) {
		t.Error("empty dir should not be a repo")
	}
	if err := Init(bare); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !IsRepo(bare) {
		t.Error("initialized dir should be a repo")
	}
}

func TestSnapshotAndHead(t *testing.T) {
	dir := newRepo(t, true)
	if sha := HeadSHA(dir); sha != "" {
		t.Errorf("HeadSHA before any commit = %q, want empty", sha)
	}
	writeFile(t, dir, "a.txt", "one\n")
	if err := Snapshot(dir, "first"); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	sha := HeadSHA(dir)
	if sha == "" {
		t.Fatal("HeadSHA empty after commit")
	}
	rp, err := RevParse(dir, "HEAD")
	if err != nil {
		t.Fatalf("RevParse: %v", err)
	}
	if rp != sha {
		t.Errorf("RevParse(HEAD)=%q != HeadSHA=%q", rp, sha)
	}
	if _, err := RevParse(dir, "definitely-not-a-ref"); err == nil {
		t.Error("RevParse of bogus ref should error")
	}
}

func TestSnapshotNoOp(t *testing.T) {
	dir := newRepo(t, true)
	// Clean tree: Snapshot must be a no-op, not an error, and create no commit.
	if err := Snapshot(dir, "nothing"); err != nil {
		t.Fatalf("Snapshot on clean tree: %v", err)
	}
	if sha := HeadSHA(dir); sha != "" {
		t.Errorf("no-op snapshot created a commit: %q", sha)
	}
}

func TestSnapshotIdentityFallback(t *testing.T) {
	// No local identity and isolated global/system config: the fallback author
	// must let the commit succeed.
	dir := newRepo(t, false)
	writeFile(t, dir, "a.txt", "x\n")
	if err := Snapshot(dir, "fallback"); err != nil {
		t.Fatalf("Snapshot without configured identity: %v", err)
	}
	author := rawGit(t, dir, "log", "-1", "--format=%ae")
	if author != "sandboxer@local" {
		t.Errorf("fallback author = %q, want sandboxer@local", author)
	}
}

func TestBranchOps(t *testing.T) {
	dir := newRepo(t, true)
	writeFile(t, dir, "a.txt", "x\n")
	if err := Snapshot(dir, "init"); err != nil {
		t.Fatal(err)
	}
	if err := CheckoutBranch(dir, "sandbox/feat"); err != nil {
		t.Fatalf("CheckoutBranch: %v", err)
	}
	br, err := CurrentBranch(dir)
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if br != "sandbox/feat" {
		t.Errorf("CurrentBranch = %q, want sandbox/feat", br)
	}
}

func TestChangedCountAndDiff(t *testing.T) {
	dir := newRepo(t, true)
	writeFile(t, dir, "a.txt", "one\n")
	if err := Snapshot(dir, "base"); err != nil {
		t.Fatal(err)
	}
	base := HeadSHA(dir)
	if n := ChangedCount(dir, base); n != 0 {
		t.Errorf("ChangedCount with no new commits = %d, want 0", n)
	}
	writeFile(t, dir, "b.txt", "two\n")
	if err := Snapshot(dir, "second"); err != nil {
		t.Fatal(err)
	}
	if n := ChangedCount(dir, base); n != 1 {
		t.Errorf("ChangedCount after adding one file = %d, want 1", n)
	}
	d, err := Diff(dir, base+"..HEAD", false)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(d, "b.txt") {
		t.Errorf("diff missing b.txt:\n%s", d)
	}
	stat, err := Diff(dir, base+"..HEAD", true)
	if err != nil {
		t.Fatalf("Diff --stat: %v", err)
	}
	if !strings.Contains(stat, "b.txt") || !strings.Contains(stat, "|") {
		t.Errorf("--stat diff unexpected:\n%s", stat)
	}
}

func TestMergeFlowCherryPick(t *testing.T) {
	// src holds the project; dest is a clone on a sandbox branch with one extra,
	// non-conflicting commit. Mirrors `sandboxer merge`.
	isolateGit(t)
	parent := t.TempDir()
	src := filepath.Join(parent, "src")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Init(src); err != nil {
		t.Fatal(err)
	}
	writeFile(t, src, "a.txt", "a\n")
	if err := Snapshot(src, "A"); err != nil {
		t.Fatal(err)
	}
	base := HeadSHA(src)

	dest := filepath.Join(parent, "dest")
	rawGit(t, parent, "clone", "-q", src, dest)
	if err := CheckoutBranch(dest, "sandbox/x"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dest, "b.txt", "b\n")
	if err := Snapshot(dest, "B"); err != nil {
		t.Fatal(err)
	}

	if err := Fetch(src, dest, "sandbox/x"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	tip, err := RevParse(src, "FETCH_HEAD")
	if err != nil {
		t.Fatalf("RevParse FETCH_HEAD: %v", err)
	}
	rng := base + ".." + tip
	if n := RevListCount(src, rng); n != 1 {
		t.Fatalf("RevListCount(%s) = %d, want 1", rng, n)
	}
	if err := CherryPick(src, rng); err != nil {
		t.Fatalf("CherryPick: %v", err)
	}
	if _, err := os.Stat(filepath.Join(src, "b.txt")); err != nil {
		t.Errorf("cherry-pick did not bring b.txt into src: %v", err)
	}
	if n := RevListCount(src, "bogus..range"); n != 0 {
		t.Errorf("RevListCount of bad range = %d, want 0", n)
	}
}

func TestCherryPickConflictAbort(t *testing.T) {
	isolateGit(t)
	parent := t.TempDir()
	src := filepath.Join(parent, "src")
	if err := os.Mkdir(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Init(src); err != nil {
		t.Fatal(err)
	}
	writeFile(t, src, "f.txt", "a\n")
	if err := Snapshot(src, "A"); err != nil {
		t.Fatal(err)
	}
	base := HeadSHA(src)

	dest := filepath.Join(parent, "dest")
	rawGit(t, parent, "clone", "-q", src, dest)
	if err := CheckoutBranch(dest, "sandbox/x"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dest, "f.txt", "b\n") // conflicting edit
	if err := Snapshot(dest, "B"); err != nil {
		t.Fatal(err)
	}

	// Diverge src so the cherry-pick conflicts.
	writeFile(t, src, "f.txt", "c\n")
	if err := Snapshot(src, "C"); err != nil {
		t.Fatal(err)
	}

	if err := Fetch(src, dest, "sandbox/x"); err != nil {
		t.Fatal(err)
	}
	tip, _ := RevParse(src, "FETCH_HEAD")
	if err := CherryPick(src, base+".."+tip); err == nil {
		CherryPickAbort(src)
		t.Fatal("expected cherry-pick conflict, got success")
	}
	CherryPickAbort(src)
	got, _ := os.ReadFile(filepath.Join(src, "f.txt"))
	if string(got) != "c\n" {
		t.Errorf("after abort f.txt = %q, want %q", got, "c\n")
	}
}

func TestFormatPatch(t *testing.T) {
	dir := newRepo(t, true)
	writeFile(t, dir, "a.txt", "a\n")
	if err := Snapshot(dir, "A"); err != nil {
		t.Fatal(err)
	}
	base := HeadSHA(dir)
	writeFile(t, dir, "a.txt", "a-changed\n")
	if err := Snapshot(dir, "B"); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	produced, err := FormatPatch(dir, base+"..HEAD", out)
	if err != nil {
		t.Fatalf("FormatPatch: %v", err)
	}
	if !produced {
		t.Error("FormatPatch reported no patches for a real change")
	}
	entries, _ := os.ReadDir(out)
	if len(entries) == 0 {
		t.Error("no patch files written")
	}
}

func TestFormatPatchNoChange(t *testing.T) {
	dir := newRepo(t, true)
	writeFile(t, dir, "a.txt", "a\n")
	if err := Snapshot(dir, "A"); err != nil {
		t.Fatal(err)
	}
	head := HeadSHA(dir)
	out := t.TempDir()
	produced, err := FormatPatch(dir, head+".."+head, out) // empty range
	if err != nil {
		t.Fatalf("FormatPatch empty range: %v", err)
	}
	if produced {
		t.Error("FormatPatch over an empty range should report no patches")
	}
}

func TestFetchError(t *testing.T) {
	dir := newRepo(t, true)
	if err := Fetch(dir, filepath.Join(t.TempDir(), "not-a-repo"), "x"); err == nil {
		t.Error("Fetch from a non-repo should error")
	}
}
