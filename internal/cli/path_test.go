package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// pathLines returns the non-empty stdout lines of a successful path run.
func pathLines(t *testing.T, args ...string) []string {
	t.Helper()
	code, out, errs := run(append([]string{"path"}, args...)...)
	if code != 0 {
		t.Fatalf("path %v = %d, %s", args, code, errs)
	}
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if s := strings.TrimSpace(l); s != "" {
			lines = append(lines, s)
		}
	}
	return lines
}

// TestPathPrintsSourceWorktree: path prints the source worktree's absolute
// host path — bare, on stdout — so it composes into `code "$(sandboxer path)"`.
func TestPathPrintsSourceWorktree(t *testing.T) {
	project := newProject(t)
	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}

	lines := pathLines(t, "feat", "--src", project)
	if len(lines) != 1 {
		t.Fatalf("path lines = %d, want 1: %v", len(lines), lines)
	}
	got := lines[0]
	if !filepath.IsAbs(got) {
		t.Errorf("path = %q, want an absolute path", got)
	}
	// The single source is the project repo itself, materialized under <slug>/
	// grouped by branch with the repo as the leaf (the scaffold seeds
	// feat/<name>).
	want := filepath.Join(sandboxDir(project, "feat"), "feat", "feat", filepath.Base(project))
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	if !isDir(got) {
		t.Errorf("path %q is not an existing directory", got)
	}
}

// TestPathSelectSource: a second positional selects one source of the sandbox
// by name (its repo directory), so a multi-source sandbox composes into a
// single command; an unknown name fails and lists the real sources, and a
// source name alongside --dir is rejected (--dir is slug-level).
func TestPathSelectSource(t *testing.T) {
	project := newProject(t)
	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}
	name := filepath.Base(project) // the single source's name

	lines := pathLines(t, "feat", name, "--src", project)
	if len(lines) != 1 {
		t.Fatalf("path feat %s lines = %d, want 1: %v", name, len(lines), lines)
	}
	if want := filepath.Join(sandboxDir(project, "feat"), "feat", "feat", name); lines[0] != want {
		t.Errorf("path feat %s = %q, want %q", name, lines[0], want)
	}

	code, _, errs := run("path", "feat", "nonesuch", "--src", project)
	if code == 0 || !strings.Contains(errs, "no source") || !strings.Contains(errs, name) {
		t.Errorf("path feat nonesuch = %d %q, want a rejection listing %q", code, errs, name)
	}

	if code, _, errs := run("path", "feat", name, "--dir", "--src", project); code == 0 || !strings.Contains(errs, "--dir") {
		t.Errorf("path feat %s --dir = %d %q, want a rejection naming --dir", name, code, errs)
	}
}

// createSandbox makes the sandbox slug in project from its own one-profile
// config file — the way a second sandbox is added beside the auto-scaffolded
// one (see TestListStateColumn).
func createSandbox(t *testing.T, project, slug string) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), slug+".nix")
	if err := os.WriteFile(cfg, []byte("{ name = \""+slug+"\"; srcs = [ { src = \".\"; branch = \"feat/"+slug+"\"; } ]; }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("create", "--src", project, "--config", cfg); code != 0 {
		t.Fatalf("create %s: %d %s", slug, code, errs)
	}
}

// TestPathActiveSandbox: with no slug, path answers for the active sandbox —
// the same getter semantics as `use`.
func TestPathActiveSandbox(t *testing.T) {
	project := newProject(t)
	for _, slug := range []string{"feat", "other"} {
		createSandbox(t, project, slug)
	}
	if code, _, errs := run("use", "other", "--src", project); code != 0 {
		t.Fatalf("use: %d %s", code, errs)
	}

	lines := pathLines(t, "--src", project)
	if len(lines) != 1 || !strings.Contains(lines[0], filepath.Join("other", "feat", "other", filepath.Base(project))) {
		t.Errorf("path (active) = %v, want the 'other' sandbox worktree", lines)
	}
}

// TestPathDir: --dir prints the mount root — the parent holding the managed
// worktrees, which is what the container sees.
func TestPathDir(t *testing.T) {
	project := newProject(t)
	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}

	lines := pathLines(t, "feat", "--src", project, "--dir")
	if len(lines) != 1 {
		t.Fatalf("path --dir lines = %d, want 1: %v", len(lines), lines)
	}
	if want := sandboxDir(project, "feat"); lines[0] != want {
		t.Errorf("path --dir = %q, want %q", lines[0], want)
	}
	// The worktree path sits under the mount root, not the other way round.
	if wt := pathLines(t, "feat", "--src", project); !strings.HasPrefix(wt[0], lines[0]+string(filepath.Separator)) {
		t.Errorf("worktree %q is not under the mount root %q", wt[0], lines[0])
	}
}

// TestPathNoSlug: with several sandboxes and none active, path refuses rather
// than guessing, and names the candidates.
func TestPathNoSlug(t *testing.T) {
	project := newProject(t)
	for _, slug := range []string{"feat", "other"} {
		createSandbox(t, project, slug)
	}
	code, _, errs := run("path", "--src", project)
	if code == 0 {
		t.Fatalf("path with no active sandbox = 0, want failure")
	}
	if !strings.Contains(errs, "no sandbox selected") {
		t.Errorf("error = %q, want it to name the ambiguity", errs)
	}
}

// TestSourcesIncludeScope: a narrowed source reports its include directories
// alongside the path, so show explains what the container actually sees (the
// worktree itself is whole — the narrowing lives in the mounts).
func TestSourcesIncludeScope(t *testing.T) {
	project := newProject(t)
	cfg := filepath.Join(t.TempDir(), "narrow.nix")
	body := "{ name = \"narrow\"; srcs = [ { src = \".\"; branch = \"feat/narrow\"; include = [ \"/sub/\" ]; } ]; }\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(project, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "sub", "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-qm", "sub"}} {
		if out, err := exec.Command("git", append([]string{"-C", project}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	if code, _, errs := run("create", "--src", project, "--config", cfg); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}

	code, out, errs := run("show", "narrow", "--src", project)
	if code != 0 {
		t.Fatalf("show = %d, %s", code, errs)
	}
	if !strings.Contains(out, "[/sub/]") {
		t.Errorf("show sources block does not report the include scope:\n%s", out)
	}
}

// TestIncludeSyntaxErrorCreatesNothing: a syntactically bad include (here:
// unanchored) is refused at config time — before any sandbox tree is
// materialized — with a message naming the accepted forms.
func TestIncludeSyntaxErrorCreatesNothing(t *testing.T) {
	project := newProject(t)
	cfg := filepath.Join(t.TempDir(), "bad.nix")
	body := "{ name = \"bad\"; srcs = [ { src = \".\"; branch = \"feat/bad\"; include = [ \"*.md\" ]; } ]; }\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errs := run("create", "--src", project, "--config", cfg)
	if code == 0 {
		t.Fatal("create accepted an unanchored include, want a refusal")
	}
	if !strings.Contains(errs, "must be anchored") {
		t.Errorf("error does not explain the accepted forms: %q", errs)
	}
	// and: nothing was created before the refusal
	if _, err := os.Stat(filepath.Join(project, "sandboxes")); !os.IsNotExist(err) {
		t.Errorf("a sandbox tree was materialized despite the bad include (err=%v)", err)
	}
}

// TestIncludePatternMatchesNoDirectory: an include pattern is valid syntax but
// must select at least one directory on the branch — a pattern matching
// nothing (here: "**/*.md" only matches a DIRECTORY named like *.md, and files
// never match) fails the create, fail-closed, naming the pattern and branch.
// Unlike a syntax error this fires AFTER the worktree is materialized: the
// expansion needs the tree on disk.
func TestIncludePatternMatchesNoDirectory(t *testing.T) {
	project := newProject(t)
	cfg := filepath.Join(t.TempDir(), "glob.nix")
	body := "{ name = \"glob\"; srcs = [ { src = \".\"; branch = \"feat/glob\"; include = [ \"**/*.md\" ]; } ]; }\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errs := run("create", "--src", project, "--config", cfg)
	if code == 0 {
		t.Fatal("create accepted a pattern matching nothing, want a refusal")
	}
	if !strings.Contains(errs, "matches no directory") ||
		!strings.Contains(errs, "**/*.md") || !strings.Contains(errs, "feat/glob") {
		t.Errorf("error does not name the pattern and branch: %q", errs)
	}
}

// TestIncludePatternVanishedMatches: patterns resolve against the live
// worktree at every mount computation, so a matched directory deleted on the
// host AFTER create fails the next mount assembly (compose/enter/exec) with
// the zero-match refusal — while `show`, which is read-only, degrades to
// skipping the freshness verdict instead of erroring.
func TestIncludePatternVanishedMatches(t *testing.T) {
	project := newProject(t)
	cfg := filepath.Join(t.TempDir(), "pat.nix")
	body := "{ name = \"pat\"; srcs = [ { src = \".\"; branch = \"feat/pat\"; include = [ \"**/proto/\" ]; } ]; }\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(project, "lib", "proto"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "lib", "proto", "a.proto"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-qm", "proto"}} {
		if out, err := exec.Command("git", append([]string{"-C", project}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	if code, _, errs := run("create", "--src", project, "--config", cfg); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}
	wt := pathLines(t, "pat", "--src", project)[0]
	if err := os.RemoveAll(filepath.Join(wt, "lib", "proto")); err != nil {
		t.Fatal(err)
	}

	code, _, errs := run("compose", "pat", "--src", project, "--backend", "podman")
	if code == 0 {
		t.Fatal("compose resolved mounts for a pattern matching nothing, want a refusal")
	}
	if !strings.Contains(errs, "matches no directory") {
		t.Errorf("compose error = %q, want the zero-match refusal", errs)
	}

	if code, _, errs := run("show", "pat", "--src", project); code != 0 {
		t.Errorf("show = %d (%s), want it to degrade gracefully, not fail", code, errs)
	}
}

// TestSourcesNoneRecorded: a sandbox with no recorded sources (never synced)
// makes path fail with a way forward instead of printing nothing, and show say
// so rather than claiming an empty source set.
func TestSourcesNoneRecorded(t *testing.T) {
	project := newProject(t)
	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}
	// Drop the sync record, leaving the sandbox as one predating the srcs model.
	if err := os.Remove(stateDir(project, "_meta", "feat.srcs.json")); err != nil {
		t.Fatal(err)
	}

	code, _, errs := run("path", "feat", "--src", project)
	if code == 0 {
		t.Errorf("path with no recorded sources = 0, want failure")
	}
	if !strings.Contains(errs, "sandboxer enter feat") {
		t.Errorf("path error = %q, want it to name the way forward", errs)
	}
	// --dir still answers: the mount root does not depend on the sync record.
	if lines := pathLines(t, "feat", "--src", project, "--dir"); len(lines) != 1 {
		t.Errorf("path --dir = %v, want the mount root", lines)
	}
	if _, out, _ := run("show", "feat", "--src", project); !strings.Contains(out, "(none recorded") {
		t.Errorf("show does not report the missing sources:\n%s", out)
	}
}

// TestShowSourcesBlock: show reports the RESOLVED sources — repo, branch and
// the host path — not just the configured srcs from the profile.
func TestShowSourcesBlock(t *testing.T) {
	project := newProject(t)
	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}

	code, out, errs := run("show", "feat", "--src", project)
	if code != 0 {
		t.Fatalf("show = %d, %s", code, errs)
	}
	if !strings.Contains(out, "== sources ==") {
		t.Fatalf("show has no sources block:\n%s", out)
	}
	wt := pathLines(t, "feat", "--src", project)[0]
	for _, want := range []string{filepath.Base(project), "feat/feat", wt} {
		if !strings.Contains(out, want) {
			t.Errorf("show sources block missing %q:\n%s", want, out)
		}
	}
}
