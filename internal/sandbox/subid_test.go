package sandbox

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestSubIDLines pins the range math: the sandbox user is handed every id of
// the outer namespace's [1..count] EXCEPT its own — podman maps that one
// itself, and a subordinate range containing it would be a double mapping
// newuidmap rejects.
func TestSubIDLines(t *testing.T) {
	cases := []struct {
		name       string
		own, count int
		want       string
	}{
		{"own inside range", 1000, 65536, "sandbox:1:999\nsandbox:1001:64536\n"},
		{"own is 1 (no low range)", 1, 65536, "sandbox:2:65535\n"},
		{"own is count (no high range)", 65536, 65536, "sandbox:1:65535\n"},
		{"own above range (small host allocation)", 70000, 65536, "sandbox:1:65536\n"},
		{"tiny allocation", 1000, 5, "sandbox:1:5\n"},
		{"zero count", 1000, 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := subIDLines(c.own, c.count); got != c.want {
				t.Errorf("subIDLines(%d, %d) = %q, want %q", c.own, c.count, got, c.want)
			}
		})
	}
}

// TestHostSubIDCount pins the /etc/subuid parsing: entries match by login name
// OR numeric uid (subuid(5) allows either), multiple entries sum, and comments/
// malformed lines/other users are skipped. A missing file reads as 0.
func TestHostSubIDCount(t *testing.T) {
	uid := os.Getuid()
	username := ""
	if u, err := user.Current(); err == nil {
		username = u.Username
	}

	write := func(t *testing.T, content string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "subuid")
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("numeric uid entry", func(t *testing.T) {
		p := write(t, strconv.Itoa(uid)+":100000:65536\n")
		if got := hostSubIDCount(p, uid); got != 65536 {
			t.Errorf("count = %d, want 65536", got)
		}
	})
	t.Run("login name entry", func(t *testing.T) {
		if username == "" {
			t.Skip("no resolvable username in this environment")
		}
		p := write(t, username+":100000:65536\n")
		if got := hostSubIDCount(p, uid); got != 65536 {
			t.Errorf("count = %d, want 65536", got)
		}
	})
	t.Run("multiple entries sum", func(t *testing.T) {
		p := write(t, strconv.Itoa(uid)+":100000:65536\n"+strconv.Itoa(uid)+":300000:1000\n")
		if got := hostSubIDCount(p, uid); got != 66536 {
			t.Errorf("count = %d, want 66536", got)
		}
	})
	t.Run("other users, comments and garbage are skipped", func(t *testing.T) {
		p := write(t, "# comment\nsomeoneelse:100000:65536\nnot-a-subid-line\n"+
			strconv.Itoa(uid)+":100000:12\n"+strconv.Itoa(uid)+":x:y\n")
		if got := hostSubIDCount(p, uid); got != 12 {
			t.Errorf("count = %d, want 12", got)
		}
	})
	t.Run("missing file reads as 0", func(t *testing.T) {
		if got := hostSubIDCount(filepath.Join(t.TempDir(), "absent"), uid); got != 0 {
			t.Errorf("count = %d, want 0", got)
		}
	})
}

// TestWriteNestedIDFiles exercises the generation end-to-end against fixture
// host databases: the four files land under _meta with the sandbox uid/gid
// named `sandbox`, and a host without ranges writes nothing — removing any
// stale files so a previous grant is never mounted again.
func TestWriteNestedIDFiles(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: the rootless model under test does not apply")
	}
	newBase := func(t *testing.T) *Base {
		t.Helper()
		b := &Base{Src: t.TempDir(), Dir: t.TempDir()}
		if err := os.MkdirAll(b.metaDir(), 0o755); err != nil {
			t.Fatal(err)
		}
		return b
	}
	setHostDBs := func(t *testing.T, subuid, subgid string) {
		t.Helper()
		dir := t.TempDir()
		origU, origG := hostSubuidPath, hostSubgidPath
		hostSubuidPath = filepath.Join(dir, "subuid")
		hostSubgidPath = filepath.Join(dir, "subgid")
		t.Cleanup(func() { hostSubuidPath, hostSubgidPath = origU, origG })
		if subuid != "" {
			if err := os.WriteFile(hostSubuidPath, []byte(subuid), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if subgid != "" {
			if err := os.WriteFile(hostSubgidPath, []byte(subgid), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	read := func(t *testing.T, p string) string {
		t.Helper()
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("expected generated file: %v", err)
		}
		return string(data)
	}

	t.Run("host with ranges generates all four files", func(t *testing.T) {
		me := strconv.Itoa(os.Getuid())
		setHostDBs(t, me+":100000:65536\n", me+":100000:65536\n")
		b := newBase(t)
		ok, err := b.WriteNestedIDFiles("sb")
		if err != nil || !ok {
			t.Fatalf("WriteNestedIDFiles = (%v, %v), want (true, nil)", ok, err)
		}
		ids := b.NestedIDFiles("sb")
		passwd := read(t, ids.Passwd)
		wantUser := "sandbox:x:" + me + ":" + strconv.Itoa(os.Getgid()) + ":sandbox:" + b.HomeDir("sb") + ":/bin/bash\n"
		if !strings.Contains(passwd, wantUser) {
			t.Errorf("passwd missing %q:\n%s", wantUser, passwd)
		}
		for _, sys := range []string{"root:x:0:0:", "nobody:x:65534:"} {
			if !strings.Contains(passwd, sys) {
				t.Errorf("passwd missing the %q system entry:\n%s", sys, passwd)
			}
		}
		group := read(t, ids.Group)
		if gid := os.Getgid(); gid != 0 && gid != 65534 {
			if !strings.Contains(group, "sandbox:x:"+strconv.Itoa(gid)+":\n") {
				t.Errorf("group missing the sandbox gid:\n%s", group)
			}
		}
		if got, want := read(t, ids.Subuid), subIDLines(os.Getuid(), 65536); got != want {
			t.Errorf("subuid = %q, want %q", got, want)
		}
		if got, want := read(t, ids.Subgid), subIDLines(os.Getgid(), 65536); got != want {
			t.Errorf("subgid = %q, want %q", got, want)
		}
	})
	t.Run("host without ranges removes stale files and reports false", func(t *testing.T) {
		me := strconv.Itoa(os.Getuid())
		setHostDBs(t, me+":100000:65536\n", me+":100000:65536\n")
		b := newBase(t)
		if ok, err := b.WriteNestedIDFiles("sb"); err != nil || !ok {
			t.Fatalf("seed write = (%v, %v), want (true, nil)", ok, err)
		}
		setHostDBs(t, "", "") // ranges gone
		ok, err := b.WriteNestedIDFiles("sb")
		if err != nil || ok {
			t.Fatalf("WriteNestedIDFiles = (%v, %v), want (false, nil)", ok, err)
		}
		ids := b.NestedIDFiles("sb")
		for _, p := range []string{ids.Passwd, ids.Group, ids.Subuid, ids.Subgid} {
			if _, err := os.Stat(p); err == nil {
				t.Errorf("stale %s survived the no-ranges rewrite", p)
			}
		}
	})
	t.Run("subgid ranges are looked up by the USER id", func(t *testing.T) {
		me := strconv.Itoa(os.Getuid())
		// subgid keyed by uid (as subgid(5) prescribes) — must be found even
		// though it is a gid database.
		setHostDBs(t, me+":100000:65536\n", me+":100000:2000\n")
		b := newBase(t)
		ok, err := b.WriteNestedIDFiles("sb")
		if err != nil || !ok {
			t.Fatalf("WriteNestedIDFiles = (%v, %v), want (true, nil)", ok, err)
		}
		if got, want := read(t, b.NestedIDFiles("sb").Subgid), subIDLines(os.Getgid(), 2000); got != want {
			t.Errorf("subgid = %q, want %q", got, want)
		}
	})
}
