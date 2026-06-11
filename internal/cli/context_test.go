package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCreateCopiesAgentContext: create copies the default agent-context files
// (CLAUDE.md, .claude) from the project root to the sandbox root, and push
// never returns them — end-to-end through the CLI.
func TestCreateCopiesAgentContext(t *testing.T) {
	project := newProject(t)
	if err := os.WriteFile(filepath.Join(project, "CLAUDE.md"), []byte("rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(project, ".claude", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".claude", "skills", "s.md"), []byte("skill\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}
	dest := filepath.Join(project, ".sandboxer", "feat")
	if got, err := os.ReadFile(filepath.Join(dest, "CLAUDE.md")); err != nil || string(got) != "rules\n" {
		t.Errorf("CLAUDE.md not copied to the sandbox root: %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dest, ".claude", "skills", "s.md")); err != nil {
		t.Errorf(".claude/ not copied to the sandbox root: %v", err)
	}

	// An agent edit to the context copy must never flow back on push.
	if err := os.WriteFile(filepath.Join(dest, "CLAUDE.md"), []byte("tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("push", "feat", "--src", project, "--force"); code != 0 {
		t.Fatalf("push: %d %s", code, errs)
	}
	if got, _ := os.ReadFile(filepath.Join(project, "CLAUDE.md")); string(got) != "rules\n" {
		t.Errorf("push wrote a context file back to the project: %q", got)
	}
}

// TestProjectRootFromSandbox: the in-container derivation only trusts the
// canonical <root>/.sandboxer/<slug> shape.
func TestProjectRootFromSandbox(t *testing.T) {
	if got := projectRootFromSandbox("/w/proj/.sandboxer/feat"); got != "/w/proj" {
		t.Errorf("canonical layout = %q, want /w/proj", got)
	}
	if got := projectRootFromSandbox("/w/elsewhere/feat"); got != "" {
		t.Errorf("non-sandbox layout = %q, want empty", got)
	}
}

// TestRecreateRefreshesContext: recreate rebuilds the sandbox, so an edited
// project CLAUDE.md lands in the fresh copy (the old copy was wiped).
func TestRecreateRefreshesContext(t *testing.T) {
	project := newProject(t)
	if err := os.WriteFile(filepath.Join(project, "CLAUDE.md"), []byte("v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := run("create", "feat", "--src", project); code != 0 {
		t.Fatalf("create: %d %s", code, errs)
	}
	if err := os.WriteFile(filepath.Join(project, "CLAUDE.md"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(project, ".sandboxer", "feat")
	stubRemoveSession(t, dest, nil)
	code, out, errs := run("recreate", "feat", "--src", project)
	if code != 0 || !strings.Contains(out, "recreated") {
		t.Fatalf("recreate = (%d, %q, %q)", code, out, errs)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "CLAUDE.md")); string(got) != "v2\n" {
		t.Errorf("recreate did not refresh the context copy: %q", got)
	}
}
