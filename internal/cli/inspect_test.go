package cli

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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

// allRowFields returns the whitespace-split fields of the host-wide list row for
// slug — the marker column included, so a row with no marker starts at the
// PROJECT field.
func allRowFields(t *testing.T, out, slug string) []string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); slices.Contains(f, slug) {
			return f
		}
	}
	t.Fatalf("no list row for %q in:\n%s", slug, out)
	return nil
}

// listRow returns the whitespace-split fields of the list row for slug.
func listRow(t *testing.T, out, slug string) []string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && f[0] == slug {
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
		if err := os.WriteFile(cfg, []byte("{ name = \""+slug+"\"; srcs = [ { src = \".\"; branch = \"feat/x\"; } ]; }\n"), 0o644); err != nil {
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
		if row := listRow(t, out, slug); row[1] != want {
			t.Errorf("list state for %s = %q, want %q (row %v)", slug, row[1], want, row)
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
		if row := listRow(t, out, "feat"); row[1] != "-" {
			t.Errorf("list state on engine error = %q, want -", row[1])
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
		t.Setenv("SANDBOXER_ENGINE", "")
		code, out, errs := run("list", "--src", project)
		if code != 0 {
			t.Fatalf("list = %d, %s", code, errs)
		}
		if called {
			t.Error("the enumeration seam must not be called without an engine")
		}
		if row := listRow(t, out, "feat"); row[1] != "-" {
			t.Errorf("engine-less list state = %q, want -", row[1])
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
		// marker, project, slug, state — the cwd project's active sandbox.
		if row := allRowFields(t, out, "feat"); row[0] != "*" || row[2] != "feat" || row[3] != "running" {
			t.Errorf("feat row = %v, want [* <project> feat running …]", row)
		}
		// The deleted project's sandbox survives in the listing, flagged.
		if row := allRowFields(t, out, "old"); row[0] != "!" || row[2] != "old" || row[3] != "-" {
			t.Errorf("old row = %v, want [! <project> old - …]", row)
		}
		if i, j := strings.Index(out, "feat"), strings.Index(out, "old"); i > j {
			t.Errorf("the current project must come first:\n%s", out)
		}
		for _, want := range []string{
			"* = active (use) in the current project.",
			"! = the project directory is gone (state left behind).",
			"--src <path>",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("legend missing %q:\n%s", want, out)
			}
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
		if row := allRowFields(t, out, "feat"); row[3] != "-" {
			t.Errorf("state on engine error = %q, want - (row %v)", row[3], row)
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

// TestProjectCol pins the PROJECT column's rendering: the home directory folds
// to ~, and without --wide a long path is truncated from the LEFT, keeping the
// tail that tells two checkouts apart.
func TestProjectCol(t *testing.T) {
	home := filepath.Join("/home", "u")
	t.Setenv("HOME", home)
	for _, c := range []struct {
		src, want string
		wide      bool
	}{
		{src: home, want: "~"},
		{src: filepath.Join(home, "work", "sandboxer"), want: filepath.Join("~", "work", "sandboxer")},
		{src: "/srv/checkout", want: "/srv/checkout"},
		{src: "/a/very/deeply/nested/place/for/a/checkout", want: "…nested/place/for/a/checkout"},
		{src: "/a/very/deeply/nested/place/for/a/checkout", want: "/a/very/deeply/nested/place/for/a/checkout", wide: true},
		// A path that is not under $HOME must not lose its prefix to the fold.
		{src: home + "-elsewhere/x", want: home + "-elsewhere/x"},
	} {
		if got := projectCol(c.src, c.wide); got != c.want {
			t.Errorf("projectCol(%q, wide=%v) = %q, want %q", c.src, c.wide, got, c.want)
		}
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

	t.Run("no engine", func(t *testing.T) {
		stubSessionSeams(t, backend.SessionInfo{}, "h")
		t.Setenv("PATH", "")
		t.Setenv("SANDBOXER_ENGINE", "")
		code, out, errs := run("show", "feat", "--src", project)
		if code != 0 {
			t.Fatalf("show = %d, %s", code, errs)
		}
		if !strings.Contains(out, "state: unknown (no container engine)") {
			t.Errorf("engine-less show missing the unknown state:\n%s", out)
		}
	})
}
