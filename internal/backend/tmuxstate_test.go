package backend

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// line assembles one captureFormat row (9 tab-separated fields) the way tmux
// would emit it, so the parser tests read as the real output shape.
func line(sess, win, wname, wactive, layout, pidx, pactive, path, cmd string) string {
	return strings.Join([]string{sess, win, wname, wactive, layout, pidx, pactive, path, cmd}, "\t")
}

func TestParseTmuxState_GroupsSessionsWindowsPanes(t *testing.T) {
	out := strings.Join([]string{
		line("main", "0", "edit", "1", "layoutA", "0", "1", "/work/a", "nvim"),
		line("main", "0", "edit", "1", "layoutA", "1", "0", "/work/b", "bash"),
		line("main", "1", "logs", "0", "layoutB", "0", "1", "/work", "less"),
		line("side", "0", "sh", "1", "layoutC", "0", "1", "/tmp", "bash"),
	}, "\n") + "\n"

	got := parseTmuxState(out)
	want := []TmuxSession{
		{Name: "main", Windows: []TmuxWindow{
			{Name: "edit", Layout: "layoutA", Active: true, Panes: []TmuxPane{
				{Path: "/work/a", Command: "nvim", Active: true},
				{Path: "/work/b", Command: "bash", Active: false},
			}},
			{Name: "logs", Layout: "layoutB", Active: false, Panes: []TmuxPane{
				{Path: "/work", Command: "less", Active: true},
			}},
		}},
		{Name: "side", Windows: []TmuxWindow{
			{Name: "sh", Layout: "layoutC", Active: true, Panes: []TmuxPane{
				{Path: "/tmp", Command: "bash", Active: true},
			}},
		}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseTmuxState mismatch:\n got %#v\nwant %#v", got, want)
	}
}

func TestParseTmuxState_SkipsMalformedAndEmpty(t *testing.T) {
	out := "\n" + "too\tfew\tfields\n" +
		line("main", "0", "w", "1", "L", "0", "1", "/x", "bash") + "\n"
	got := parseTmuxState(out)
	if len(got) != 1 || len(got[0].Windows) != 1 || len(got[0].Windows[0].Panes) != 1 {
		t.Fatalf("expected one clean session past the malformed rows, got %#v", got)
	}
	if got[0].Windows[0].Panes[0].Path != "/x" {
		t.Fatalf("wrong pane survived: %#v", got[0].Windows[0].Panes[0])
	}
}

func TestParseTmuxState_Empty(t *testing.T) {
	if got := parseTmuxState(""); got != nil {
		t.Fatalf("empty capture should parse to nil, got %#v", got)
	}
}

func TestTmuxRestoreScript_EmptyIsPlainAttach(t *testing.T) {
	got := TmuxRestoreScript(nil, "main")
	want := "exec tmux -L 'sandboxer' new-session -A -s 'main'"
	if got != want {
		t.Fatalf("empty restore should be the plain attach:\n got %q\nwant %q", got, want)
	}
}

func TestTmuxRestoreScript_RebuildsGuardedAndAttaches(t *testing.T) {
	sessions := []TmuxSession{
		{Name: "main", Windows: []TmuxWindow{
			{Name: "edit", Layout: "L0", Active: false, Panes: []TmuxPane{
				{Path: "/work/a"}, {Path: "/work/b"},
			}},
			{Name: "logs", Layout: "L1", Active: true, Panes: []TmuxPane{
				{Path: "/work"},
			}},
		}},
	}
	s := TmuxRestoreScript(sessions, "main")

	for _, want := range []string{
		"B=$(tmux -L 'sandboxer' show-options -gv base-index",
		"if ! tmux -L 'sandboxer' has-session -t '=main' 2>/dev/null; then",
		"tmux -L 'sandboxer' new-session -d -s 'main' -c '/work/a'",
		"tmux -L 'sandboxer' rename-window -t 'main':$((B+0)) 'edit'",
		"tmux -L 'sandboxer' split-window -t 'main':$((B+0)) -c '/work/b'",
		"tmux -L 'sandboxer' select-layout -t 'main':$((B+0)) 'L0'",
		"tmux -L 'sandboxer' new-window -t 'main':$((B+1)) -n 'logs' -c '/work'",
		"tmux -L 'sandboxer' select-window -t 'main':$((B+1))", // window 'logs' is active
		"exec tmux -L 'sandboxer' new-session -A -s 'main'",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("restore script missing %q in:\n%s", want, s)
		}
	}
	// A single-pane window must NOT emit select-layout (tmux rejects a mismatch).
	if strings.Contains(s, "select-layout -t 'main':$((B+1))") {
		t.Fatalf("select-layout emitted for a lone-pane window:\n%s", s)
	}
}

func TestTmuxRestoreScript_QuotesHostileNamesAndPaths(t *testing.T) {
	sessions := []TmuxSession{
		{Name: "a'b; rm -rf /", Windows: []TmuxWindow{
			{Name: "w", Panes: []TmuxPane{{Path: "/has space/and'quote"}}},
		}},
	}
	s := TmuxRestoreScript(sessions, "main")
	// The single quote in the name must be broken out, never left to open a word.
	if !strings.Contains(s, `'a'\''b; rm -rf /'`) {
		t.Fatalf("session name not shell-escaped:\n%s", s)
	}
	if !strings.Contains(s, `-c '/has space/and'\''quote'`) {
		t.Fatalf("pane path not shell-escaped:\n%s", s)
	}
}

func TestShquote(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"plain", "'plain'"},
		{"", "''"},
		{"with space", "'with space'"},
		{"it's", `'it'\''s'`},
	} {
		if got := shquote(tc.in); got != tc.want {
			t.Errorf("shquote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTmuxStateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.session.json")
	want := []TmuxSession{
		{Name: "main", Windows: []TmuxWindow{
			{Name: "w", Layout: "L", Active: true, Panes: []TmuxPane{{Path: "/x", Command: "bash", Active: true}}},
		}},
	}
	if err := WriteTmuxState(path, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := ReadTmuxState(path); !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch:\n got %#v\nwant %#v", got, want)
	}
}

func TestWriteTmuxState_NilWritesEmptyArray(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.session.json")
	if err := WriteTmuxState(path, nil); err != nil {
		t.Fatalf("write nil: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.TrimSpace(string(data)) != "[]" {
		t.Fatalf("nil should persist as an empty array, got %q", string(data))
	}
	if got := ReadTmuxState(path); len(got) != 0 {
		t.Fatalf("empty-array state should read as no sessions, got %#v", got)
	}
}

func TestReadTmuxState_AbsentIsNil(t *testing.T) {
	if got := ReadTmuxState(filepath.Join(t.TempDir(), "nope.json")); got != nil {
		t.Fatalf("absent state should read as nil, got %#v", got)
	}
}

func TestTmuxRestoreScript_NoTrailingNewline(t *testing.T) {
	s := TmuxRestoreScript([]TmuxSession{
		{Name: "main", Windows: []TmuxWindow{{Panes: []TmuxPane{{Path: "/x"}}}}},
	}, "main")
	if strings.HasSuffix(s, "\n") {
		t.Fatalf("restore script must not end in a newline (it is spliced before `; else`):\n%q", s)
	}
	if !strings.HasSuffix(s, "new-session -A -s 'main'") {
		t.Fatalf("restore script must end with the attach:\n%q", s)
	}
}

func TestCaptureTmuxState_EngineFailureIsNil(t *testing.T) {
	if got := CaptureTmuxState("sandboxer-no-such-engine-xyz", "c"); got != nil {
		t.Fatalf("a failing engine must capture nil, got %#v", got)
	}
}

func TestSaveSessionState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.session.json")
	// No path → never saves.
	if SaveSessionState("docker", "c", "") {
		t.Error("empty statePath must not save")
	}
	// A failed capture (bogus engine) reports no save and writes no file — an
	// earlier saved layout must never be overwritten with emptiness.
	if SaveSessionState("sandboxer-no-such-engine-xyz", "c", path) {
		t.Error("a failed capture must not report a save")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a failed capture must not create the state file")
	}
}
