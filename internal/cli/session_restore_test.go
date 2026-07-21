package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/backend"
)

// TestTmuxRestoreArgs_NoStateIsPlainEnter: with no saved layout, restore args
// are exactly the plain enter attach — so callers use them unconditionally.
func TestTmuxRestoreArgs_NoStateIsPlainEnter(t *testing.T) {
	got := tmuxRestoreArgs("main", filepath.Join(t.TempDir(), "absent.json"))
	want := tmuxEnterArgs("main")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("no saved state should yield the plain enter attach:\n got %q\nwant %q", got, want)
	}
}

// TestTmuxRestoreArgs_RunsRebuildSequence writes a saved layout, then EXECUTES
// the generated attach against a stub `tmux` on PATH. This verifies the script
// is syntactically valid bash AND issues the expected rebuild commands before
// the final attach — engine-free, no container and no real tmux. It is the
// closest an engine-free test gets to proving the restore actually works.
func TestTmuxRestoreArgs_RunsRebuildSequence(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	statePath := filepath.Join(dir, "s.session.json")
	if err := backend.WriteTmuxState(statePath, []backend.TmuxSession{
		{Name: "main", Windows: []backend.TmuxWindow{
			{Name: "edit", Layout: "L0", Panes: []backend.TmuxPane{{Path: "/work/a"}, {Path: "/work/b"}}},
			{Name: "logs", Active: true, Panes: []backend.TmuxPane{{Path: "/work"}}},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	// Stub tmux: logs every invocation; `has-session` fails so the rebuild runs,
	// `show-options` prints a base-index of 0.
	log := filepath.Join(dir, "calls.log")
	stub := "#!/usr/bin/env bash\n" +
		"echo \"$*\" >> " + shArg(log) + "\n" +
		"case \"$*\" in *has-session*) exit 1;; *show-options*) echo 0;; esac\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}

	args := tmuxRestoreArgs("main", statePath)
	if len(args) != 3 || args[0] != "bash" || args[1] != "-c" {
		t.Fatalf("unexpected attach args: %q", args)
	}
	cmd := exec.Command(args[0], args[1], args[2])
	cmd.Env = withPathPrepended(dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("restore script failed to run (syntax error?): %v\n%s", err, out)
	}

	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("stub tmux was never invoked: %v", err)
	}
	calls := string(data)
	for _, want := range []string{
		"has-session -t =main",
		"new-session -d -s main -c /work/a",
		"rename-window -t main:0 edit",
		"split-window -t main:0 -c /work/b",
		"select-layout -t main:0 L0",
		"new-window -t main:1 -n logs -c /work",
		"select-window -t main:1", // window 'logs' (index 1) was active
		"new-session -A -s main",  // the final attach
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("expected the restore to call tmux with %q; full log:\n%s", want, calls)
		}
	}
}

// shArg single-quotes a value for the stub script (test paths are tmp dirs, but
// keep it safe anyway).
func shArg(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// withPathPrepended returns the process environment with dir prepended to PATH
// (so a stub binary there shadows the real one), PATH de-duplicated to the one
// entry the child shell will actually use.
func withPathPrepended(dir string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "PATH=") {
			env = append(env, kv)
		}
	}
	return append(env, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
