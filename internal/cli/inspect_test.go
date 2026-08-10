package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/irasikhin/sandboxer/internal/backend"
	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/sandbox"
)

// stubSessionStates replaces the list/doctor enumeration seam with a fixed
// result, restoring it when the test ends.
func stubSessionStates(t *testing.T, states map[string]string, err error) {
	t.Helper()
	old := sessionStates
	t.Cleanup(func() { sessionStates = old })
	sessionStates = func(engine, baseDir string) (map[string]string, error) {
		return states, err
	}
}

// stubAllSessionStates replaces the HOST-WIDE enumeration seam (baseDir → slug
// → status) with a fixed result, restoring it when the test ends.
func stubAllSessionStates(t *testing.T, states map[string]map[string]string, err error) {
	t.Helper()
	old := allSessionStates
	t.Cleanup(func() { allSessionStates = old })
	allSessionStates = func(engine string) (map[string]map[string]string, error) {
		return states, err
	}
}

// rowFields splits a list row into fields with the marker column NORMALIZED:
// index 0 is always the marker ("" when the row carries none), so the columns
// after it keep the same indices whether or not a row is flagged.
func rowFields(line string) []string {
	f := strings.Fields(line)
	marker := ""
	if len(f) > 0 && (f[0] == "*" || f[0] == "!") {
		marker, f = f[0], f[1:]
	}
	return append([]string{marker}, f...)
}

// allRowFields returns the host-wide list row for slug as
// [marker, ID, PROJECT, SANDBOX, STATE].
func allRowFields(t *testing.T, out, slug string) []string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if f := rowFields(line); len(f) > 3 && f[3] == slug {
			return f
		}
	}
	t.Fatalf("no list row for %q in:\n%s", slug, out)
	return nil
}

// listRow returns the per-project list row for slug as
// [marker, ID, SANDBOX, STATE].
func listRow(t *testing.T, out, slug string) []string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if f := rowFields(line); len(f) > 2 && f[2] == slug {
			return f
		}
	}
	t.Fatalf("no list row for %q in:\n%s", slug, out)
	return nil
}

// TestListStateColumn: the STATE column folds engine statuses — "running"
// stays, any other recorded status reads "stopped", no container is "-".
func TestListStateColumn(t *testing.T) {
	project := sessionProject(t)
	for _, slug := range []string{"idle", "off"} {
		cfg := filepath.Join(t.TempDir(), slug+".nix")
		if err := os.WriteFile(cfg, []byte("{ name = \""+slug+"\"; srcs = [ { src = \".\"; branch = \"feat/"+slug+"\"; } ]; }\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if code, _, errs := run("create", "--src", project, "--config", cfg); code != 0 {
			t.Fatalf("create %s: %d %s", slug, code, errs)
		}
	}
	stubSessionStates(t, map[string]string{"feat": "running", "idle": "exited"}, nil)

	code, out, errs := run("list", "--src", project)
	if code != 0 {
		t.Fatalf("list = %d, %s", code, errs)
	}
	if !strings.Contains(out, "STATE") {
		t.Errorf("list header missing STATE:\n%s", out)
	}
	for slug, want := range map[string]string{"feat": "running", "idle": "stopped", "off": "-"} {
		if row := listRow(t, out, slug); row[3] != want {
			t.Errorf("list state for %s = %q, want %q (row %v)", slug, row[3], want, row)
		}
	}
}

// TestListStateBestEffort: enumeration failures and engine-less hosts both
// degrade to dashes so the listing never fails on the session probe.
func TestListStateBestEffort(t *testing.T) {
	t.Run("engine error", func(t *testing.T) {
		project := sessionProject(t)
		stubSessionStates(t, nil, errors.New("engine on fire"))
		code, out, errs := run("list", "--src", project)
		if code != 0 {
			t.Fatalf("list = %d, %s", code, errs)
		}
		if row := listRow(t, out, "feat"); row[3] != "-" {
			t.Errorf("list state on engine error = %q, want -", row[3])
		}
	})

	t.Run("no engine skips the probe", func(t *testing.T) {
		project := sessionProject(t)
		called := false
		stubSessionStates(t, nil, nil)
		sessionStates = func(engine, baseDir string) (map[string]string, error) {
			called = true
			return nil, nil
		}
		t.Setenv("PATH", "")
		t.Setenv("SANDBOXER_MSB", "/nonexistent/msb-xyz")
		t.Setenv("SANDBOXER_SMOLVM", "/nonexistent/smolvm-xyz")
		code, out, errs := run("list", "--src", project)
		if code != 0 {
			t.Fatalf("list = %d, %s", code, errs)
		}
		if called {
			t.Error("the enumeration seam must not be called without an engine")
		}
		if row := listRow(t, out, "feat"); row[3] != "-" {
			t.Errorf("engine-less list state = %q, want -", row[3])
		}
	})
}

// TestListAllProjects: a bare `list` is HOST-WIDE — every project in the state
// root, not just the one in the cwd. The current project comes first and owns
// the '*' marker (a bare enter/exec can only target that one), while a project
// whose directory was deleted behind sandboxer's back still shows its leftover
// sandboxes, marked '!'.
func TestListAllProjects(t *testing.T) {
	// Isolate from the suite-wide state root: every other test's project lives
	// there, and a host-wide listing would enumerate all of them.
	t.Setenv("SANDBOXER_STATE", t.TempDir())
	project := sessionProject(t) // creates the "feat" sandbox
	if code, _, errs := run("use", "feat", "--src", project); code != 0 {
		t.Fatalf("use feat: %d %s", code, errs)
	}
	deleted := newProject(t)
	if code, _, errs := run("create", "old", "--src", deleted); code != 0 {
		t.Fatalf("create old: %d %s", code, errs)
	}
	if err := os.RemoveAll(deleted); err != nil {
		t.Fatal(err)
	}
	stubAllSessionStates(t, map[string]map[string]string{
		config.StateDir(project): {"feat": "running"},
	}, nil)
	t.Chdir(project)

	t.Run("every project, current first", func(t *testing.T) {
		code, out, errs := run("list")
		if code != 0 {
			t.Fatalf("list = %d, %s", code, errs)
		}
		if !strings.Contains(out, "PROJECT") {
			t.Errorf("the host-wide listing has no PROJECT column:\n%s", out)
		}
		// marker, id, project, slug, state — the cwd project's active sandbox.
		if row := allRowFields(t, out, "feat"); row[0] != "*" || row[4] != "running" {
			t.Errorf("feat row = %v, want [* <id> <project> feat running …]", row)
		}
		// The deleted project's sandbox survives in the listing, flagged.
		if row := allRowFields(t, out, "old"); row[0] != "!" || row[4] != "-" {
			t.Errorf("old row = %v, want [! <id> <project> old - …]", row)
		}
		if i, j := strings.Index(out, "feat"), strings.Index(out, "old"); i > j {
			t.Errorf("the current project must come first:\n%s", out)
		}
		for _, want := range []string{
			"* = active (use) in the current project.",
			"! = the project directory is gone (state left behind;",
			"--src <path>",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("legend missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("the ID column is the host-wide handle", func(t *testing.T) {
		code, out, errs := run("list")
		if code != 0 {
			t.Fatalf("list = %d, %s", code, errs)
		}
		for slug, src := range map[string]string{"feat": project, "old": deleted} {
			want := sandbox.ID(config.StateDir(src), slug)
			if got := allRowFields(t, out, slug)[1]; got != want {
				t.Errorf("%s id column = %q, want %q (the id commands resolve)", slug, got, want)
			}
		}
		if !strings.Contains(out, "ID") {
			t.Errorf("the listing has no ID header:\n%s", out)
		}
	})

	t.Run("wide keeps the full project path", func(t *testing.T) {
		code, out, errs := run("list", "-w")
		if code != 0 {
			t.Fatalf("list -w = %d, %s", code, errs)
		}
		if !strings.Contains(out, project) {
			t.Errorf("list -w does not show the full project path %s:\n%s", project, out)
		}
	})

	t.Run("engine failures degrade to dashes", func(t *testing.T) {
		stubAllSessionStates(t, nil, errors.New("engine on fire"))
		code, out, errs := run("list")
		if code != 0 {
			t.Fatalf("list = %d, %s", code, errs)
		}
		if row := allRowFields(t, out, "feat"); row[4] != "-" {
			t.Errorf("state on engine error = %q, want - (row %v)", row[4], row)
		}
	})

	t.Run("src narrows back to one project", func(t *testing.T) {
		code, out, errs := run("list", "--src", project)
		if code != 0 {
			t.Fatalf("list --src = %d, %s", code, errs)
		}
		if strings.Contains(out, "PROJECT") || strings.Contains(out, "old") {
			t.Errorf("--src must list one project, without the PROJECT column:\n%s", out)
		}
	})
}

// TestProjectPath pins the PROJECT column's rendering: the home directory folds
// to ~ and the path is otherwise untouched — how much of it fits is the table's
// call (projectWidth), not the path's.
func TestProjectPath(t *testing.T) {
	home := filepath.Join("/home", "u")
	t.Setenv("HOME", home)
	for _, c := range []struct{ src, want string }{
		{src: home, want: "~"},
		{src: filepath.Join(home, "work", "sandboxer"), want: filepath.Join("~", "work", "sandboxer")},
		{src: "/srv/checkout", want: "/srv/checkout"},
		{src: "/a/very/deeply/nested/place/for/a/checkout", want: "/a/very/deeply/nested/place/for/a/checkout"},
		// A path that is not under $HOME must not lose its prefix to the fold.
		{src: home + "-elsewhere/x", want: home + "-elsewhere/x"},
	} {
		if got := projectPath(c.src); got != c.want {
			t.Errorf("projectPath(%q) = %q, want %q", c.src, got, c.want)
		}
	}
}

// TestProjectWidth: the PROJECT column is as wide as the longest path — full
// paths are the DEFAULT, since a path that says which repo but not where is
// what made the column useless. It shrinks only against a real terminal that
// cannot fit it, never below the floor, and never when the output is being read
// by something (term 0: a pipe, a file, a test) or --wide was asked for.
func TestProjectWidth(t *testing.T) {
	for _, c := range []struct {
		desc          string
		longest, term int
		wide          bool
		want          int
	}{
		{desc: "no terminal keeps the full path", longest: 60, term: 0, want: 60},
		{desc: "a wide terminal fits it", longest: 60, term: 200, want: 60},
		{desc: "a short path is never padded up", longest: 12, term: 200, want: 12},
		{desc: "a narrow terminal cuts the path back", longest: 60, term: 80, want: 80 - fixedListCols},
		{desc: "the floor holds on a tiny terminal", longest: 60, term: 40, want: minProjectCol},
		{desc: "--wide ignores the terminal", longest: 60, term: 40, wide: true, want: 60},
	} {
		if got := projectWidth(c.longest, c.term, c.wide); got != c.want {
			t.Errorf("%s: projectWidth(%d, %d, %v) = %d, want %d",
				c.desc, c.longest, c.term, c.wide, got, c.want)
		}
	}
}

// TestTruncateLeft: a path too long for its column loses its HEAD, keeping the
// tail that tells two checkouts apart, and the ellipsis counts as one rune.
func TestTruncateLeft(t *testing.T) {
	for _, c := range []struct {
		s    string
		n    int
		want string
	}{
		{s: "/a/b/c", n: 6, want: "/a/b/c"},
		{s: "/a/b/c", n: 10, want: "/a/b/c"},
		{s: "/a/very/deeply/nested/checkout", n: 12, want: "…ed/checkout"},
		{s: "/a/b/c", n: 0, want: "/a/b/c"},
	} {
		if got := truncateLeft(c.s, c.n); got != c.want || utf8.RuneCountInString(got) > max(c.n, len(c.s)) {
			t.Errorf("truncateLeft(%q, %d) = %q, want %q", c.s, c.n, got, c.want)
		}
	}
}

// TestOutWidth: only a real terminal has a width to fit into. A capture buffer
// has none, and neither does a character device that is not a tty (/dev/null —
// the file that makes a naive isatty lie), so both report 0 and nothing gets
// truncated behind a pipe.
func TestOutWidth(t *testing.T) {
	if got := outWidth(&bytes.Buffer{}); got != 0 {
		t.Errorf("outWidth(buffer) = %d, want 0", got)
	}
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Skipf("no %s: %v", os.DevNull, err)
	}
	defer f.Close()
	if got := outWidth(f); got != 0 {
		t.Errorf("outWidth(%s) = %d, want 0", os.DevNull, got)
	}
}

// TestOrderProjects: the project the user stands in leads the listing (its
// sandboxes are the ones a bare command targets); everything else keeps the
// path order, and a cwd that is no project reorders nothing.
func TestOrderProjects(t *testing.T) {
	projects := []sandbox.Project{
		{Base: &sandbox.Base{Src: "/a"}},
		{Base: &sandbox.Base{Src: "/b"}},
		{Base: &sandbox.Base{Src: "/c"}},
	}
	for wd, want := range map[string][]string{
		"/b":         {"/b", "/a", "/c"},
		"/a":         {"/a", "/b", "/c"},
		"/elsewhere": {"/a", "/b", "/c"},
	} {
		var got []string
		for _, p := range orderProjects(projects, wd) {
			got = append(got, p.Src)
		}
		if !slices.Equal(got, want) {
			t.Errorf("orderProjects(wd=%s) = %v, want %v", wd, got, want)
		}
	}
}

// TestListJSON: --json emits machine-readable rows — absolute project paths,
// no truncation, the state and each project's own active pointer — and an
// empty host is [], never a hint string.
func TestListJSON(t *testing.T) {
	t.Setenv("SANDBOXER_STATE", t.TempDir())

	var empty []listEntry
	code, out, errs := run("list", "--json")
	if code != 0 || json.Unmarshal([]byte(out), &empty) != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty list --json = (%d, %q, %s), want a [] array", code, out, errs)
	}

	project := sessionProject(t)
	if code, _, errs := run("use", "feat", "--src", project); code != 0 {
		t.Fatalf("use feat: %d %s", code, errs)
	}
	stubAllSessionStates(t, map[string]map[string]string{
		config.StateDir(project): {"feat": "running"},
	}, nil)

	code, out, errs = run("list", "--json")
	if code != 0 {
		t.Fatalf("list --json = %d, %s", code, errs)
	}
	var entries []listEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("list --json is not valid JSON: %v\n%s", err, out)
	}
	if len(entries) != 1 {
		t.Fatalf("list --json entries = %d, want 1:\n%s", len(entries), out)
	}
	e := entries[0]
	if e.Sandbox != "feat" || e.Project != project || e.State != "running" || !e.Active || e.ProjectGone {
		t.Errorf("list --json entry = %+v", e)
	}
	if e.ID != sandbox.ID(config.StateDir(project), e.Sandbox) {
		t.Errorf("list --json id = %q, want the host-wide handle", e.ID)
	}

	// --src narrows the JSON listing the same way it narrows the table.
	code, out, errs = run("list", "--src", project, "--json")
	if code != 0 {
		t.Fatalf("list --src --json = %d, %s", code, errs)
	}
	entries = nil
	if err := json.Unmarshal([]byte(out), &entries); err != nil || len(entries) != 1 {
		t.Errorf("list --src --json = (%v, %d entries):\n%s", err, len(entries), out)
	}
}

// TestListAllEmpty: with no project state at all the host-wide listing prints a
// hint, not a header with nothing under it.
func TestListAllEmpty(t *testing.T) {
	t.Setenv("SANDBOXER_STATE", t.TempDir())
	code, out, errs := run("list")
	if code != 0 {
		t.Fatalf("list = %d, %s", code, errs)
	}
	if !strings.Contains(out, "no sandboxes on this host") {
		t.Errorf("empty host listing = %q, want the create hint", out)
	}
}

// TestShowSessionBlock: show reports the session container name, its state,
// and the fresh/stale verdict from the recorded vs recomputed config hash.
// TestShowJSON: --json emits one object carrying what the text sections say —
// the stored resolved profile verbatim, the recorded sources with their host
// paths, and the session verdict with the tri-state freshness.
func TestShowJSON(t *testing.T) {
	project := sessionProject(t)
	stubSessionSeams(t, backend.SessionInfo{Exists: true, Running: true, Hash: "h"}, "h")

	code, out, errs := run("show", "feat", "--src", project, "--json")
	if code != 0 {
		t.Fatalf("show --json = %d, %s", code, errs)
	}
	var doc struct {
		Slug    string         `json:"slug"`
		Backend string         `json:"backend"`
		Profile map[string]any `json:"profile"`
		Sources []showSource   `json:"sources"`
		Session showSession    `json:"session"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("show --json is not valid JSON: %v\n%s", err, out)
	}
	if doc.Slug != "feat" || doc.Profile == nil {
		t.Errorf("show --json slug/profile = %q/%v", doc.Slug, doc.Profile)
	}
	if len(doc.Sources) == 0 || doc.Sources[0].Branch == "" || doc.Sources[0].Path == "" {
		t.Errorf("show --json sources = %+v, want the recorded worktrees", doc.Sources)
	}
	if doc.Session.State != "running" || doc.Session.Fresh == nil || !*doc.Session.Fresh {
		t.Errorf("show --json session = %+v, want running+fresh", doc.Session)
	}
	if strings.Contains(out, "== profile") {
		t.Error("show --json must not carry the text section headers")
	}
}

func TestShowSessionBlock(t *testing.T) {
	project := sessionProject(t)
	name := backend.SessionName("feat", config.StateDir(project))

	cases := []struct {
		desc string
		info backend.SessionInfo
		want string
	}{
		{"running fresh", backend.SessionInfo{Exists: true, Running: true, Hash: "h"}, "state: running (fresh)"},
		{"stopped fresh", backend.SessionInfo{Exists: true, Running: false, Hash: "h"}, "state: stopped (fresh)"},
		{"running stale", backend.SessionInfo{Exists: true, Running: true, Hash: "old"}, "state: running (stale"},
		{"no container", backend.SessionInfo{}, "state: none"},
	}
	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			stubSessionSeams(t, c.info, "h")
			code, out, errs := run("show", "feat", "--src", project)
			if code != 0 {
				t.Fatalf("show = %d, %s", code, errs)
			}
			for _, want := range []string{"== session ==", "container: " + name, c.want} {
				if !strings.Contains(out, want) {
					t.Errorf("show output missing %q:\n%s", want, out)
				}
			}
		})
	}

	t.Run("running image-stale", func(t *testing.T) {
		// The hash still matches, but the engine's image was rebuilt under the
		// same tag — show must name the image, not the profile.
		stubSessionSeams(t, backend.SessionInfo{Exists: true, Running: true, Hash: "h", ImageID: "old"}, "h")
		backendImageID = func(engine, image string) string { return "new" }
		code, out, errs := run("show", "feat", "--src", project)
		if code != 0 {
			t.Fatalf("show = %d, %s", code, errs)
		}
		if !strings.Contains(out, "state: running (stale — the image was rebuilt") {
			t.Errorf("show output missing the image-rebuilt verdict:\n%s", out)
		}
	})

	t.Run("no runner", func(t *testing.T) {
		stubSessionSeams(t, backend.SessionInfo{}, "h")
		t.Setenv("PATH", "")
		t.Setenv("SANDBOXER_MSB", "/nonexistent/msb-xyz")
		t.Setenv("SANDBOXER_SMOLVM", "/nonexistent/smolvm-xyz")
		code, out, errs := run("show", "feat", "--src", project)
		if code != 0 {
			t.Fatalf("show = %d, %s", code, errs)
		}
		if !strings.Contains(out, "state: unknown (no microVM runner installed)") {
			t.Errorf("runner-less show missing the unknown state:\n%s", out)
		}
	})
}
