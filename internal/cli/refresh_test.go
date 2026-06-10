package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPullRefreshesSnapshot pins the fix for a deps edit never reaching an
// existing sandbox: profile.json is written once at create time, so editing
// .sandboxer.yaml afterwards used to be invisible to pull. Now a mutating op
// (here host-side `pull`) re-resolves the live profile via syncSnapshot, so
// newly-listed deps are vendored in. Engine-free — exercises only the host copy
// path, no container.
func TestPullRefreshesSnapshot(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	t.Setenv("SANDBOXER_PROFILES", t.TempDir())

	project := t.TempDir()
	t.Chdir(project)

	// A dep that exists under the project root, but is NOT yet listed.
	if err := os.MkdirAll(filepath.Join(project, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "lib", "util.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := filepath.Join(project, ".sandboxer.yaml")
	writeConfig := func(body string) {
		if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Create with a deps-less profile → snapshot frozen with no deps.
	writeConfig("profiles:\n  feat: {}\n")
	if code, _, errs := run("create", "feat"); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}
	if _, err := os.Stat(filepath.Join(project, ".sandboxer", "feat", "lib")); err == nil {
		t.Fatal("lib should not be vendored before it is listed")
	}

	// Add roots + deps, then pull: the snapshot must refresh and copy the dep.
	writeConfig("profiles:\n  feat:\n    roots: [\".\"]\n    deps: [\"lib\"]\n")
	code, out, errs := run("pull", "feat")
	if code != 0 {
		t.Fatalf("pull: %d %s", code, errs)
	}
	vendored := filepath.Join(project, ".sandboxer", "feat", "lib", "util.txt")
	if got, err := os.ReadFile(vendored); err != nil || string(got) != "v1\n" {
		t.Fatalf("dep not vendored after pull: out=%q err=%v", out, err)
	}

	// The stored snapshot now reflects the edited profile.
	snap, err := os.ReadFile(filepath.Join(project, ".sandboxer", "_meta", "feat.profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(snap), `"lib"`) {
		t.Errorf("profile.json not refreshed with the new dep: %s", snap)
	}

	// A second pull is idempotent — the existing copy is KEPT.
	if code, out, _ := run("pull", "feat"); code != 0 || !strings.Contains(out, "KEEP") {
		t.Errorf("second pull = (%d, %q), want KEEP", code, out)
	}
}

// TestEnterExecRefreshVendorsNewDeps covers the enter/exec snapshot-refresh
// branches: a deps edit made after creation is vendored in before the sandbox is
// entered. An invalid backend makes both commands fail right after the refresh,
// so the branch is exercised without a container engine — the proof is that the
// new dep landed in the sandbox.
func TestEnterExecRefreshVendorsNewDeps(t *testing.T) {
	t.Setenv("SANDBOXER_IN_CONTAINER", "")
	t.Setenv("SANDBOXER_PROFILES", t.TempDir())

	project := t.TempDir()
	t.Chdir(project)
	if err := os.MkdirAll(filepath.Join(project, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "lib", "util.txt"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := filepath.Join(project, ".sandboxer.yaml")
	if err := os.WriteFile(cfg, []byte("profiles:\n  en: {}\n  ex: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"en", "ex"} {
		if code, _, errs := run("create", slug); code != 0 {
			t.Fatalf("create %s: %d %s", slug, code, errs)
		}
	}

	// Add the dep to both profiles, then enter/exec — each refreshes its snapshot.
	body := "profiles:\n" +
		"  en:\n    roots: [\".\"]\n    deps: [\"lib\"]\n" +
		"  ex:\n    roots: [\".\"]\n    deps: [\"lib\"]\n"
	if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// An invalid backend fails after the refresh branch has run.
	if code, _, _ := run("enter", "en", "--backend", "native"); code != 1 {
		t.Errorf("enter with invalid backend exit = %d, want 1", code)
	}
	if _, err := os.Stat(filepath.Join(project, ".sandboxer", "en", "lib", "util.txt")); err != nil {
		t.Errorf("enter did not vendor the new dep: %v", err)
	}

	if code, _, _ := run("exec", "ex", "--backend", "native", "--", "true"); code != 1 {
		t.Errorf("exec with invalid backend exit = %d, want 1", code)
	}
	if _, err := os.Stat(filepath.Join(project, ".sandboxer", "ex", "lib", "util.txt")); err != nil {
		t.Errorf("exec did not vendor the new dep: %v", err)
	}
}
