package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/backend"
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
		cfg := filepath.Join(t.TempDir(), slug+".yaml")
		if err := os.WriteFile(cfg, []byte("name: "+slug+"\n"), 0o644); err != nil {
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

// TestShowSessionBlock: show reports the session container name, its state,
// and the fresh/stale verdict from the recorded vs recomputed config hash.
func TestShowSessionBlock(t *testing.T) {
	project := sessionProject(t)
	name := backend.SessionName("feat", filepath.Join(project, ".sandboxer"))

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

// TestShowSessionUnknownMCP: a profile whose mcp: entry cannot be resolved
// leaves the freshness verdict unjudged (plain state, no fresh/stale claim)
// instead of failing show — the same degrade as an unresolvable image.
func TestShowSessionUnknownMCP(t *testing.T) {
	project := newProject(t)
	fakePodman(t)
	cfg := filepath.Join(t.TempDir(), "p.yaml")
	if err := os.WriteFile(cfg, []byte("name: feat\nmcp: [no-such-server]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("create", "--src", project, "--config", cfg); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}
	stubSessionSeams(t, backend.SessionInfo{Exists: true, Running: true, Hash: "h"}, "h")

	code, out, errs := run("show", "--src", project, "--config", cfg)
	if code != 0 {
		t.Fatalf("show = %d, %s", code, errs)
	}
	if !strings.Contains(out, "state: running\n") || strings.Contains(out, "fresh") || strings.Contains(out, "stale") {
		t.Errorf("unjudged state expected, got:\n%s", out)
	}
}
