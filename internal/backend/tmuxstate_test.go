package backend

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/registry"
)

// resumeOf is the test shorthand for a Last-only resume spec map.
func resumeOf(agent string, last ...string) map[string]registry.ResumeSpec {
	return map[string]registry.ResumeSpec{agent: {Last: last}}
}

// line assembles one captureFormat row (10 tab-separated fields) the way tmux
// would emit it, so the parser tests read as the real output shape.
func line(sess, win, wname, wactive, layout, pidx, pactive, path, cmd, pid string) string {
	return strings.Join([]string{sess, win, wname, wactive, layout, pidx, pactive, path, cmd, pid}, "\t")
}

func TestParseTmuxState_GroupsSessionsWindowsPanes(t *testing.T) {
	out := strings.Join([]string{
		line("main", "0", "edit", "1", "layoutA", "0", "1", "/work/a", "nvim", "101"),
		line("main", "0", "edit", "1", "layoutA", "1", "0", "/work/b", "bash", "102"),
		line("main", "1", "logs", "0", "layoutB", "0", "1", "/work", "less", "103"),
		line("side", "0", "sh", "1", "layoutC", "0", "1", "/tmp", "bash", "104"),
	}, "\n") + "\n"

	got := parseTmuxState(out)
	want := []TmuxSession{
		{Name: "main", Windows: []TmuxWindow{
			{Name: "edit", Layout: "layoutA", Active: true, Panes: []TmuxPane{
				{Path: "/work/a", Command: "nvim", Active: true, pid: 101},
				{Path: "/work/b", Command: "bash", Active: false, pid: 102},
			}},
			{Name: "logs", Layout: "layoutB", Active: false, Panes: []TmuxPane{
				{Path: "/work", Command: "less", Active: true, pid: 103},
			}},
		}},
		{Name: "side", Windows: []TmuxWindow{
			{Name: "sh", Layout: "layoutC", Active: true, Panes: []TmuxPane{
				{Path: "/tmp", Command: "bash", Active: true, pid: 104},
			}},
		}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseTmuxState mismatch:\n got %#v\nwant %#v", got, want)
	}
}

// TestParseTmuxState_NineFieldFloor: a row without the trailing pane_pid (an
// older tmux, or a pre-pid capture replayed through the parser) still parses —
// the pid is simply zero and agent detection is skipped for that pane.
func TestParseTmuxState_NineFieldFloor(t *testing.T) {
	out := strings.Join([]string{"main", "0", "w", "1", "L", "0", "1", "/x", "bash"}, "\t") + "\n"
	got := parseTmuxState(out)
	if len(got) != 1 || len(got[0].Windows[0].Panes) != 1 {
		t.Fatalf("nine-field row should parse, got %#v", got)
	}
	if p := got[0].Windows[0].Panes[0]; p.Path != "/x" || p.pid != 0 {
		t.Fatalf("nine-field pane = %#v, want /x with pid 0", p)
	}
}

func TestParseTmuxState_SkipsMalformedAndEmpty(t *testing.T) {
	out := "\n" + "too\tfew\tfields\n" +
		line("main", "0", "w", "1", "L", "0", "1", "/x", "bash", "9") + "\n"
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
	got := TmuxRestoreScript(nil, "main", nil)
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
	s := TmuxRestoreScript(sessions, "main", nil)

	for _, want := range []string{
		"if ! tmux -L 'sandboxer' has-session -t '=main' 2>/dev/null; then",
		"tmux -L 'sandboxer' new-session -d -s 'main' -c '/work/a'",
		"B=$(tmux -L 'sandboxer' display-message -p -t 'main' '#{window_index}' 2>/dev/null); B=${B:-0}",
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
	// With no resume map, the script types nothing into any pane.
	if strings.Contains(s, "send-keys") {
		t.Fatalf("nil resume map must not emit send-keys:\n%s", s)
	}
}

// TestTmuxRestoreScript_ResumesRecordedAgents: a pane whose Agent has a resume
// argv gets the command TYPED into it — a literal send-keys plus a separate
// Enter — right after the pane is created (before select-layout, targeting the
// window while the new pane is still its active one). Panes without a recorded
// agent, and agents absent from the map, stay plain shells.
func TestTmuxRestoreScript_ResumesRecordedAgents(t *testing.T) {
	sessions := []TmuxSession{
		{Name: "main", Windows: []TmuxWindow{
			{Name: "edit", Layout: "L0", Panes: []TmuxPane{
				{Path: "/work/a", Agent: "claude"},
				{Path: "/work/b"}, // plain shell — no send-keys
			}},
			{Name: "side", Panes: []TmuxPane{
				{Path: "/work/c", Agent: "codex"}, // not in the map — no send-keys
			}},
		}},
	}
	s := TmuxRestoreScript(sessions, "main", resumeOf("claude", "claude", "--continue"))

	wantType := "tmux -L 'sandboxer' send-keys -t 'main':$((B+0)) -l 'claude --continue'"
	wantEnter := "tmux -L 'sandboxer' send-keys -t 'main':$((B+0)) Enter"
	for _, want := range []string{wantType, wantEnter} {
		if !strings.Contains(s, want) {
			t.Fatalf("restore script missing %q in:\n%s", want, s)
		}
	}
	// Exactly one resumed pane → exactly two send-keys commands.
	if got := strings.Count(s, "send-keys"); got != 2 {
		t.Fatalf("send-keys emitted %d times, want 2:\n%s", got, s)
	}
	// The relaunch happens after the agent pane is created and before the
	// window's layout replay, while the new pane is the active one.
	create := strings.Index(s, "new-session -d -s 'main' -c '/work/a'")
	typed := strings.Index(s, wantType)
	layout := strings.Index(s, "select-layout -t 'main':$((B+0))")
	if create >= typed || typed >= layout {
		t.Fatalf("send-keys must land between pane creation and select-layout:\n%s", s)
	}
}

// TestTmuxRestoreScript_ResumeInSplitPane: an agent recorded in a NON-first
// pane is typed right after its split-window, not into pane 0.
func TestTmuxRestoreScript_ResumeInSplitPane(t *testing.T) {
	sessions := []TmuxSession{
		{Name: "main", Windows: []TmuxWindow{
			{Name: "edit", Layout: "L0", Panes: []TmuxPane{
				{Path: "/work/a"},
				{Path: "/work/b", Agent: "claude"},
			}},
		}},
	}
	s := TmuxRestoreScript(sessions, "main", resumeOf("claude", "claude", "--continue"))
	split := strings.Index(s, "split-window -t 'main':$((B+0)) -c '/work/b'")
	typed := strings.Index(s, "send-keys -t 'main':$((B+0)) -l 'claude --continue'")
	layout := strings.Index(s, "select-layout")
	if split < 0 || split >= typed || typed >= layout {
		t.Fatalf("split-pane resume must follow its split-window and precede select-layout:\n%s", s)
	}
}

// TestTmuxRestoreScript_HostileResumeQuoted: a resume word outside the bare
// alphabet is quoted per word by shjoin AND the whole typed line stays one
// safely-quoted script word — it can never break out of the generated bash.
func TestTmuxRestoreScript_HostileResumeQuoted(t *testing.T) {
	sessions := []TmuxSession{
		{Name: "main", Windows: []TmuxWindow{
			{Panes: []TmuxPane{{Path: "/x", Agent: "evil"}}},
		}},
	}
	s := TmuxRestoreScript(sessions, "main", resumeOf("evil", "agent", "--note", "it's; rm -rf /"))
	want := `send-keys -t 'main':$((B+0)) -l 'agent --note '\''it'\''\'\'''\''s; rm -rf /'\'''`
	if !strings.Contains(s, want) {
		t.Fatalf("hostile resume not double-quoted, want %q in:\n%s", want, s)
	}
}

// TestTmuxRestoreScript_AmbiguousSameDirGetsPicker: several panes of one agent
// in the SAME directory cannot each resume "the latest" conversation — they
// would all open the same one. Those panes get the agent's picker command;
// counting spans windows AND tmux sessions (one shared $HOME), while a pane of
// the same agent in another directory keeps the exact Last command.
func TestTmuxRestoreScript_AmbiguousSameDirGetsPicker(t *testing.T) {
	sessions := []TmuxSession{
		{Name: "main", Windows: []TmuxWindow{
			{Name: "w1", Panes: []TmuxPane{{Path: "/work/a", Agent: "claude"}}},
			{Name: "w2", Panes: []TmuxPane{{Path: "/work/b", Agent: "claude"}}},
		}},
		{Name: "side", Windows: []TmuxWindow{
			// The same directory as main:w1 — in a DIFFERENT tmux session.
			{Name: "w", Panes: []TmuxPane{{Path: "/work/a", Agent: "claude"}}},
		}},
	}
	resume := map[string]registry.ResumeSpec{"claude": {
		Last: []string{"claude", "--continue"},
		Pick: []string{"claude", "--resume"},
	}}
	s := TmuxRestoreScript(sessions, "main", resume)

	// The two /work/a panes are ambiguous → picker; the lone /work/b pane is
	// exact → continue.
	if got := strings.Count(s, "-l 'claude --resume'"); got != 2 {
		t.Fatalf("picker typed %d times, want 2 (both /work/a panes):\n%s", got, s)
	}
	if got := strings.Count(s, "-l 'claude --continue'"); got != 1 {
		t.Fatalf("continue typed %d times, want 1 (the /work/b pane):\n%s", got, s)
	}

	// Without a declared picker the ambiguous panes fall back to Last — the
	// pre-picker degradation, never a silent shell.
	s = TmuxRestoreScript(sessions, "main", resumeOf("claude", "claude", "--continue"))
	if got := strings.Count(s, "-l 'claude --continue'"); got != 3 {
		t.Fatalf("no picker declared: continue typed %d times, want 3:\n%s", got, s)
	}
}

func TestShjoin(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want string
	}{
		{[]string{"claude", "--continue"}, "claude --continue"},
		{[]string{"codex", "resume", "--last"}, "codex resume --last"},
		{[]string{"a b", "it's"}, `'a b' 'it'\''s'`},
		{[]string{}, ""},
	} {
		if got := shjoin(tc.in); got != tc.want {
			t.Errorf("shjoin(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTmuxRestoreScript_QuotesHostileNamesAndPaths(t *testing.T) {
	sessions := []TmuxSession{
		{Name: "a'b; rm -rf /", Windows: []TmuxWindow{
			{Name: "w", Panes: []TmuxPane{{Path: "/has space/and'quote"}}},
		}},
	}
	s := TmuxRestoreScript(sessions, "main", nil)
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
			{Name: "w", Layout: "L", Active: true, Panes: []TmuxPane{
				{Path: "/x", Command: "claude", Active: true, Agent: "claude"},
			}},
		}},
	}
	if err := WriteTmuxState(path, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := ReadTmuxState(path); !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip mismatch:\n got %#v\nwant %#v", got, want)
	}
	// The transient capture pid never reaches disk.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "pid") {
		t.Fatalf("pane pid must not be persisted:\n%s", data)
	}
}

// TestReadTmuxState_OldSchemaCompat: a session.json written before the agent
// field existed still reads and restores — the pane simply has no Agent.
func TestReadTmuxState_OldSchemaCompat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.session.json")
	old := `[{"name":"main","windows":[{"name":"w","layout":"L","active":true,` +
		`"panes":[{"path":"/x","command":"bash","active":true}]}]}]`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ReadTmuxState(path)
	if len(got) != 1 || got[0].Windows[0].Panes[0].Agent != "" {
		t.Fatalf("old-schema state should read with empty Agent, got %#v", got)
	}
	if s := TmuxRestoreScript(got, "main", resumeOf("claude", "claude", "--continue")); strings.Contains(s, "send-keys") {
		t.Fatalf("old-schema pane must restore as a plain shell:\n%s", s)
	}
}

func TestParsePS(t *testing.T) {
	out := "    1     0 /sbin/init\n" +
		"  100     1 -bash\n" +
		"  200   100 /nix/store/aaa/bin/node /nix/store/bbb/bin/claude --continue\n" +
		"garbage line\n" +
		"  x     1 bad pid\n" +
		"\n"
	got := parsePS(out)
	if len(got) != 3 {
		t.Fatalf("parsePS kept %d rows, want 3: %#v", len(got), got)
	}
	want200 := proc{pid: 200, ppid: 100, argv: []string{"/nix/store/aaa/bin/node", "/nix/store/bbb/bin/claude", "--continue"}}
	if !reflect.DeepEqual(got[200], want200) {
		t.Fatalf("parsePS[200] = %#v, want %#v", got[200], want200)
	}
	if got[100].ppid != 1 || got[100].argv[0] != "-bash" {
		t.Fatalf("parsePS[100] = %#v", got[100])
	}
}

// TestMatchAgent pins the deliberately narrow matching rule: argv[0] basename,
// or an interpreter's argv[1] basename — never "any argv word", because a false
// match auto-runs a command in the user's shell.
func TestMatchAgent(t *testing.T) {
	bins := map[string]string{"claude": "claude", "codex": "codex"}
	for _, tc := range []struct {
		desc string
		argv []string
		want string
	}{
		{"compiled agent", []string{"codex", "exec"}, "codex"},
		{"title rewritten to bin", []string{"claude", "--continue"}, "claude"},
		{"nix shebang shape", []string{"/nix/store/aaa/bin/node", "/nix/store/bbb/bin/claude"}, "claude"},
		{"bun shebang shape", []string{"bun", "/usr/local/bin/codex"}, "codex"},
		{"python interpreter", []string{"python3.12", "/opt/bin/claude"}, "claude"},
		{"raw script path", []string{"node", "/nix/store/bbb/cli.js"}, ""},
		{"pager on a file named claude", []string{"less", "claude"}, ""},
		{"git arg named claude", []string{"git", "log", "--", "claude"}, ""},
		{"plain shell", []string{"-bash"}, ""},
		{"empty argv", nil, ""},
	} {
		if got := matchAgent(proc{argv: tc.argv}, bins); got != tc.want {
			t.Errorf("%s: matchAgent(%q) = %q, want %q", tc.desc, tc.argv, got, tc.want)
		}
	}
}

// TestResolveAgents pins the tree walk: nearest match to the pane wins (the
// process the user launched, not the agent's own children), `exec claude`
// (pane pid IS the agent) matches at level zero, and a dead/unknown pid or a
// cyclic ppid snapshot degrades to no agent.
func TestResolveAgents(t *testing.T) {
	bins := map[string]string{"claude": "claude", "codex": "codex"}
	sessions := func(pids ...int) []TmuxSession {
		panes := make([]TmuxPane, len(pids))
		for i, pid := range pids {
			panes[i] = TmuxPane{pid: pid}
		}
		return []TmuxSession{{Name: "m", Windows: []TmuxWindow{{Panes: panes}}}}
	}

	t.Run("shell child, agent children ignored", func(t *testing.T) {
		procs := map[int]proc{
			100: {pid: 100, ppid: 1, argv: []string{"-bash"}},
			200: {pid: 200, ppid: 100, argv: []string{"node", "/nix/store/x/bin/claude"}},
			// claude's own helper children — deeper, must not shadow it even
			// though one of them would match another agent.
			300: {pid: 300, ppid: 200, argv: []string{"node", "/mcp/server.js"}},
			310: {pid: 310, ppid: 200, argv: []string{"codex", "mcp"}},
		}
		s := sessions(100)
		resolveAgents(s, procs, bins)
		if got := s[0].Windows[0].Panes[0].Agent; got != "claude" {
			t.Fatalf("Agent = %q, want claude", got)
		}
	})

	t.Run("exec'd agent is the pane pid", func(t *testing.T) {
		procs := map[int]proc{100: {pid: 100, ppid: 1, argv: []string{"claude"}}}
		s := sessions(100)
		resolveAgents(s, procs, bins)
		if got := s[0].Windows[0].Panes[0].Agent; got != "claude" {
			t.Fatalf("Agent = %q, want claude", got)
		}
	})

	t.Run("plain shell and dead pid stay empty", func(t *testing.T) {
		procs := map[int]proc{100: {pid: 100, ppid: 1, argv: []string{"-bash"}}}
		s := sessions(100, 999)
		resolveAgents(s, procs, bins)
		for i, p := range s[0].Windows[0].Panes {
			if p.Agent != "" {
				t.Errorf("pane %d Agent = %q, want empty", i, p.Agent)
			}
		}
	})

	t.Run("cyclic ppid snapshot terminates empty", func(t *testing.T) {
		procs := map[int]proc{
			100: {pid: 100, ppid: 200, argv: []string{"-bash"}},
			200: {pid: 200, ppid: 100, argv: []string{"sh"}},
		}
		s := sessions(100)
		resolveAgents(s, procs, bins)
		if got := s[0].Windows[0].Panes[0].Agent; got != "" {
			t.Fatalf("Agent = %q, want empty on a cycle", got)
		}
	})
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
	}, "main", nil)
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

// TestCaptureTmuxState_TagsAgents: the capture resolves each pane's registry
// agent from ONE extra ps listing — and a failed listing degrades to an
// untagged layout, never to a lost capture.
func TestCaptureTmuxState_TagsAgents(t *testing.T) {
	requireExec(t, "sh")
	panes := line("main", "0", "edit", "1", "L0", "0", "1", "/work", "node", "100") + "\n" +
		line("main", "0", "edit", "1", "L0", "1", "0", "/work", "bash", "110")
	ps := "  100     1 -bash\n" +
		"  105   100 /nix/store/aaa/bin/node /nix/store/bbb/bin/claude\n" +
		"  110     1 -bash"

	t.Run("tags the claude pane only", func(t *testing.T) {
		engine, logPath := sessionEngine(t)
		t.Setenv("SBX_PANES_OUT", panes)
		t.Setenv("SBX_PSEO_OUT", ps)
		got := CaptureTmuxState(engine, "c")
		if len(got) != 1 || len(got[0].Windows[0].Panes) != 2 {
			t.Fatalf("capture = %#v", got)
		}
		if a := got[0].Windows[0].Panes[0].Agent; a != "claude" {
			t.Errorf("pane 0 Agent = %q, want claude", a)
		}
		if a := got[0].Windows[0].Panes[1].Agent; a != "" {
			t.Errorf("pane 1 Agent = %q, want empty", a)
		}
		if lines := engineLog(t, logPath); !hasLine(lines, "exec c ps -eo pid=,ppid=,args=") {
			t.Errorf("missing the one ps listing:\n%s", strings.Join(lines, "\n"))
		}
	})

	t.Run("ps failure keeps the layout untagged", func(t *testing.T) {
		engine, _ := sessionEngine(t)
		t.Setenv("SBX_PANES_OUT", panes)
		t.Setenv("SBX_PSEO_FAIL", "1")
		got := CaptureTmuxState(engine, "c")
		if len(got) != 1 || len(got[0].Windows[0].Panes) != 2 {
			t.Fatalf("layout must survive a ps failure: %#v", got)
		}
		for i, p := range got[0].Windows[0].Panes {
			if p.Agent != "" {
				t.Errorf("pane %d Agent = %q, want empty after ps failure", i, p.Agent)
			}
		}
	})

	t.Run("no pane pids skips the ps exec", func(t *testing.T) {
		engine, logPath := sessionEngine(t)
		t.Setenv("SBX_PANES_OUT", strings.Join([]string{"main", "0", "w", "1", "L", "0", "1", "/x", "bash"}, "\t"))
		if got := CaptureTmuxState(engine, "c"); len(got) != 1 {
			t.Fatalf("nine-field capture = %#v", got)
		}
		if lines := engineLog(t, logPath); findPrefixLine(lines, "exec c ps") != "" {
			t.Errorf("pid-less capture must not run ps:\n%s", strings.Join(lines, "\n"))
		}
	})
}

// TestSyncSessionState pins the post-enter refresh semantics: a live layout is
// saved, a POSITIVELY idle server resets the save to [] (the user ended every
// session on purpose — the next enter must be fresh, as the exit banner
// promises), and an unexplained engine failure keeps the last good save.
func TestSyncSessionState(t *testing.T) {
	requireExec(t, "sh")
	seed := func(t *testing.T) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "s.session.json")
		if err := WriteTmuxState(path, []TmuxSession{
			{Name: "old", Windows: []TmuxWindow{{Panes: []TmuxPane{{Path: "/old"}}}}},
		}); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("live layout refreshes the save", func(t *testing.T) {
		engine, _ := sessionEngine(t)
		path := seed(t)
		t.Setenv("SBX_PANES_OUT", line("main", "0", "w", "1", "L", "0", "1", "/new", "bash", "0"))
		SyncSessionState(engine, "c", path)
		got := ReadTmuxState(path)
		if len(got) != 1 || got[0].Name != "main" || got[0].Windows[0].Panes[0].Path != "/new" {
			t.Fatalf("save not refreshed: %#v", got)
		}
	})

	t.Run("positive idleness resets to empty", func(t *testing.T) {
		engine, _ := sessionEngine(t)
		path := seed(t)
		// Every exec fails with tmux's own "no server" answer: the capture is
		// empty AND the idleness probe positively confirms it.
		t.Setenv("SBX_FAIL_ON", "exec")
		t.Setenv("SBX_STDERR", "no server running on /tmp/tmux-1000/sandboxer")
		SyncSessionState(engine, "c", path)
		if got := ReadTmuxState(path); len(got) != 0 {
			t.Fatalf("deliberate exit must reset the save, got %#v", got)
		}
		data, err := os.ReadFile(path)
		if err != nil || strings.TrimSpace(string(data)) != "[]" {
			t.Fatalf("reset must persist as [], got %q (%v)", data, err)
		}
	})

	t.Run("unexplained failure keeps the last save", func(t *testing.T) {
		engine, _ := sessionEngine(t)
		path := seed(t)
		t.Setenv("SBX_FAIL_ON", "exec")
		t.Setenv("SBX_STDERR", "cannot connect to the engine")
		SyncSessionState(engine, "c", path)
		if got := ReadTmuxState(path); len(got) != 1 || got[0].Name != "old" {
			t.Fatalf("engine failure must keep the last save, got %#v", got)
		}
	})

	t.Run("empty statePath is a no-op", func(t *testing.T) {
		engine, logPath := sessionEngine(t)
		SyncSessionState(engine, "c", "")
		if lines := engineLog(t, logPath); lines != nil {
			t.Fatalf("no state path, no engine calls:\n%s", strings.Join(lines, "\n"))
		}
	})
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
