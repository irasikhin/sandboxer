package backend

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSessionName(t *testing.T) {
	// Sanitization: every rune outside [a-zA-Z0-9_.-] becomes '-'.
	for slug, want := range map[string]string{
		"my-box":    "my-box",
		"a b":       "a-b",
		"x/y:z":     "x-y-z",
		"ok_Name.1": "ok_Name.1",
		"héllo":     "h-llo",
		"":          "",
	} {
		name := SessionName(slug, "/base")
		prefix, suffix := "sandboxer-"+want+"-", name[strings.LastIndex(name, "-")+1:]
		if !strings.HasPrefix(name, prefix) {
			t.Errorf("SessionName(%q) = %q, want prefix %q", slug, name, prefix)
		}
		// The suffix is exactly the first 8 hex chars of sha256(baseDir).
		if len(suffix) != 8 || strings.Trim(suffix, "0123456789abcdef") != "" {
			t.Errorf("SessionName(%q) suffix = %q, want 8 hex chars", slug, suffix)
		}
	}

	// Deterministic: same inputs, same name.
	if a, b := SessionName("s", "/p/.sandboxer"), SessionName("s", "/p/.sandboxer"); a != b {
		t.Errorf("SessionName not stable: %q vs %q", a, b)
	}
	// Different baseDir → different name (same slug, different project).
	if a, b := SessionName("s", "/p1/.sandboxer"), SessionName("s", "/p2/.sandboxer"); a == b {
		t.Errorf("SessionName identical across base dirs: %q", a)
	}
}

// guestEngine installs a fake msb CLI whose `exec` answers with canned
// output, pointing SANDBOXER_MSB at it, and returns the engine identity plus
// the invocation log path. It is the guest-reader stub the tmux
// capture/idleness tests drive (the lifecycle fake in msb_test.go runs real
// commands instead). Canned behaviour comes from env vars:
//
//	SBX_FAIL_ON          subcommand to fail ("exec"), with SBX_STDERR on stderr
//	SBX_PANES_OUT        stdout of the `exec … list-panes` capture
//	SBX_PSEO_OUT         stdout of the `exec … ps -eo` listing;
//	                     SBX_PSEO_FAIL makes it exit 1
//	SBX_EXEC_OUT         stdout of any other `exec`
func guestEngine(t *testing.T) (engine, logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	bin := filepath.Join(dir, "msb")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "` + logPath + `"
[ -n "$SBX_FAIL_ON" ] && [ "$1" = "$SBX_FAIL_ON" ] && { [ -n "$SBX_STDERR" ] && printf '%s\n' "$SBX_STDERR" >&2; exit 1; }
case "$*" in
*list-panes*) printf '%s\n' "$SBX_PANES_OUT" ;;
*"ps -eo"*)
  [ -n "$SBX_PSEO_FAIL" ] && exit 1
  printf '%s\n' "$SBX_PSEO_OUT" ;;
*) printf '%s\n' "$SBX_EXEC_OUT" ;;
esac
exit 0
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SANDBOXER_MSB", bin)
	return msbEngine, logPath
}

// engineLog returns the stub's invocations, one argv per line (nil before the
// first call).
func engineLog(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func hasLine(lines []string, exact string) bool { return slices.Contains(lines, exact) }

func findPrefixLine(lines []string, prefix string) string {
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	return ""
}

// TestSessionIdle pins the positive-finding stance: only an empty listing or
// tmux's own "no server running" answer count as idle; any other failure is
// "busy" (the safe answer).
func TestSessionIdle(t *testing.T) {
	requireExec(t, "sh")

	t.Run("empty listing is idle", func(t *testing.T) {
		engine, logPath := guestEngine(t)
		t.Setenv("SBX_EXEC_OUT", "")
		if !SessionIdle(engine, "c") {
			t.Error("an empty tmux listing must read as idle")
		}
		if lines := engineLog(t, logPath); !hasLine(lines, "exec c -- tmux -L sandboxer list-sessions") {
			t.Errorf("idleness probe argv missing:\n%s", strings.Join(lines, "\n"))
		}
	})

	t.Run("a live session is busy", func(t *testing.T) {
		engine, _ := guestEngine(t)
		t.Setenv("SBX_EXEC_OUT", "main: 1 windows (created ...)")
		if SessionIdle(engine, "c") {
			t.Error("a listed session must read as busy")
		}
	})

	t.Run("no server running is the definitive idle", func(t *testing.T) {
		engine, _ := guestEngine(t)
		t.Setenv("SBX_FAIL_ON", "exec")
		t.Setenv("SBX_STDERR", "no server running on /tmp/tmux-1000/sandboxer")
		if !SessionIdle(engine, "c") {
			t.Error("tmux's own no-server answer must read as idle")
		}
	})

	t.Run("an unexplained failure is busy", func(t *testing.T) {
		engine, _ := guestEngine(t)
		t.Setenv("SBX_FAIL_ON", "exec")
		t.Setenv("SBX_STDERR", "cannot reach the machine")
		if SessionIdle(engine, "c") {
			t.Error("an unexplained engine failure must read as busy — false idle destroys an agent")
		}
	})
}

// TestPlanSession pins the full pure policy table: every combination of
// exists/running/hash-freshness/idleness maps to exactly one action.
func TestPlanSession(t *testing.T) {
	const want = "h-want"
	hash := func(fresh bool) string {
		if fresh {
			return want
		}
		return "h-stale"
	}
	rows := []struct {
		exists, running, fresh, idle bool
		action                       sessionAction
	}{
		// No machine: create, whatever the other dimensions claim.
		{false, false, false, false, actCreate},
		{false, false, false, true, actCreate},
		{false, false, true, false, actCreate},
		{false, false, true, true, actCreate},
		{false, true, false, false, actCreate},
		{false, true, false, true, actCreate},
		{false, true, true, false, actCreate},
		{false, true, true, true, actCreate},
		// Stopped: start when fresh, recreate when stale.
		{true, false, false, false, actRecreate},
		{true, false, false, true, actRecreate},
		{true, false, true, false, actStart},
		{true, false, true, true, actStart},
		// Running: exec when fresh; stale always recreates (sandboxer tracks
		// no in-guest clients — multiplexing is the user's own business).
		{true, true, false, false, actRecreate},
		{true, true, false, true, actRecreate},
		{true, true, true, false, actExec},
		{true, true, true, true, actExec},
	}
	for _, r := range rows {
		info := SessionInfo{Exists: r.exists, Running: r.running, Hash: hash(r.fresh)}
		if got := planSession(info, want, ""); got != r.action {
			t.Errorf("planSession(exists=%v running=%v fresh=%v) = %d, want %d",
				r.exists, r.running, r.fresh, got, r.action)
		}
	}
	// A missing hash ("" — e.g. a machine with a lost record) is stale, never
	// fresh.
	if got := planSession(SessionInfo{Exists: true, Running: false}, want, ""); got != actRecreate {
		t.Errorf("missing hash = %d, want actRecreate", got)
	}
}

// TestPlanSessionImageStaleness pins the image-ID half of freshness: a
// hash-fresh machine whose image was rebuilt under the same tag (the
// recorded ID no longer matches the store's current one) is stale — same
// decision table as a profile change — while an unknown ID on either side
// skips the check, and the comparison tolerates a "sha256:" prefix.
func TestPlanSessionImageStaleness(t *testing.T) {
	const want = "h-want"
	rows := []struct {
		desc           string
		got, wantImage string
		running        bool
		action         sessionAction
	}{
		{"running rebuilt", "old", "new", true, actRecreate},
		{"stopped rebuilt", "old", "new", false, actRecreate},
		{"recorded ID unknown", "", "new", true, actExec},
		{"image absent locally", "old", "", true, actExec},
		{"prefix-tolerant match", "sha256:abc", "abc", true, actExec},
	}
	for _, r := range rows {
		info := SessionInfo{Exists: true, Running: r.running, Hash: want, ImageID: r.got}
		if got := planSession(info, want, r.wantImage); got != r.action {
			t.Errorf("%s: planSession = %d, want %d", r.desc, got, r.action)
		}
	}
	// A stopped machine with a matching hash and image simply starts.
	info := SessionInfo{Exists: true, Hash: want, ImageID: "abc"}
	if got := planSession(info, want, "sha256:abc"); got != actStart {
		t.Errorf("stopped fresh-by-image = %d, want actStart", got)
	}
	// A stale hash dominates: the image matching cannot make a session fresh.
	info = SessionInfo{Exists: true, Running: true, Hash: "h-stale", ImageID: "abc"}
	if got := planSession(info, want, "abc"); got != actRecreate {
		t.Errorf("stale hash with a fresh image = %d, want actRecreate", got)
	}
}

// TestImageFresh pins the pure comparison: empty on either side skips, and the
// "sha256:" prefix never causes a false stale.
func TestImageFresh(t *testing.T) {
	for _, r := range []struct {
		got, want string
		fresh     bool
	}{
		{"", "", true},
		{"", "abc", true},
		{"abc", "", true},
		{"abc", "abc", true},
		{"sha256:abc", "abc", true},
		{"abc", "sha256:abc", true},
		{"sha256:abc", "sha256:abc", true},
		{"abc", "def", false},
		{"sha256:abc", "sha256:def", false},
	} {
		if got := ImageFresh(r.got, r.want); got != r.fresh {
			t.Errorf("ImageFresh(%q, %q) = %v, want %v", r.got, r.want, got, r.fresh)
		}
	}
}

// TestStaleReason: a hash mismatch reads as a profile change; a hash-fresh
// session that still went stale can only mean the image was rebuilt.
func TestStaleReason(t *testing.T) {
	if got := staleReason(SessionInfo{Hash: "h"}, "h"); got != "image rebuilt" {
		t.Errorf("staleReason(fresh hash) = %q, want %q", got, "image rebuilt")
	}
	if got := staleReason(SessionInfo{Hash: "old"}, "h"); got != "profile changed" {
		t.Errorf("staleReason(stale hash) = %q, want %q", got, "profile changed")
	}
}
