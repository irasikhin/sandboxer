package cli

import (
	"os"
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
	// grouped by repo and named by branch (the scaffold seeds feat/<name>).
	want := filepath.Join(sandboxDir(project, "feat"), filepath.Base(project), "feat", "feat")
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	if !isDir(got) {
		t.Errorf("path %q is not an existing directory", got)
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
	if len(lines) != 1 || !strings.Contains(lines[0], filepath.Join("other", filepath.Base(project), "feat", "other")) {
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

// TestSourcesIncludeScope: a narrowed source reports its include patterns
// alongside the path, so show explains why the worktree holds only some files.
func TestSourcesIncludeScope(t *testing.T) {
	project := newProject(t)
	cfg := filepath.Join(t.TempDir(), "narrow.nix")
	body := "{ name = \"narrow\"; srcs = [ { src = \".\"; branch = \"feat/narrow\"; include = [ \"/f.txt\" ]; } ]; }\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("create", "--src", project, "--config", cfg); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}

	code, out, errs := run("show", "narrow", "--src", project)
	if code != 0 {
		t.Fatalf("show = %d, %s", code, errs)
	}
	if !strings.Contains(out, "[/f.txt]") {
		t.Errorf("show sources block does not report the include scope:\n%s", out)
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
