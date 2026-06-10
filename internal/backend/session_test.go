package backend

import (
	"bytes"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
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

// TestCreateArgv pins the exact create argv: detached + named + labeled, the
// shared commonArgs block, then the image and the keep-alive command — and
// deliberately NO --rm, no -i/-t, no timeout wrapper and no o.Args.
func TestCreateArgv(t *testing.T) {
	o := RunOpts{
		Engine: "podman", Image: "img:1", Dest: "/d", Slug: "s", BaseDir: "/b",
		HomeDir: "/d/.home", Mem: "2G", CPU: "150%", Wall: "60", Interactive: true,
		Args:  []string{"bash", "-l"},
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{},
	}
	got := CreateArgv(o, "sandboxer-s-deadbeef", "abc123")
	userns := strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
	want := []string{
		"run", "-d", "--init", "--name", "sandboxer-s-deadbeef",
		"--label", "sandboxer.managed=true",
		"--label", "sandboxer.slug=s",
		"--label", "sandboxer.base=/b",
		"--label", "sandboxer.hash=abc123",
		"--user", userns, "--cap-drop=ALL", "--security-opt", "no-new-privileges",
		"--workdir", "/d", "--volume", "/d:/d:rw",
		"--env", "SANDBOXER_IN_CONTAINER=1",
		"--env", "SANDBOXER_SLUG=s", "--env", "SANDBOXER_SANDBOX_DIR=/d",
		"--env", "HOME=/d/.home", "--volume", "/d/.home:/d/.home:rw",
		"--userns=keep-id",
		"--add-host=host.docker.internal:host-gateway",
		"--add-host=host.containers.internal:host-gateway",
		"--memory", "2G", "--cpus", "1.5",
		"img:1", "sleep", "infinity",
	}
	if !slices.Equal(got, want) {
		t.Errorf("CreateArgv =\n%q\nwant\n%q", got, want)
	}
}

// TestExecArgv pins the exact exec argv, with and without a host TERM. A
// non-TTY stdin/stdout (test buffers) must not produce -t.
func TestExecArgv(t *testing.T) {
	o := RunOpts{Dest: "/d", Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}}

	t.Setenv("TERM", "xterm-256color")
	got := ExecArgv(o, "n", []string{"claude", "--continue"})
	want := []string{"exec", "-i", "-w", "/d", "--env", "TERM=xterm-256color", "n", "claude", "--continue"}
	if !slices.Equal(got, want) {
		t.Errorf("ExecArgv with TERM =\n%q\nwant\n%q", got, want)
	}

	t.Setenv("TERM", "")
	got = ExecArgv(o, "n", []string{"sh"})
	want = []string{"exec", "-i", "-w", "/d", "n", "sh"}
	if !slices.Equal(got, want) {
		t.Errorf("ExecArgv without TERM =\n%q\nwant\n%q", got, want)
	}
}

func TestConfigHash(t *testing.T) {
	base := RunOpts{
		Engine: "docker", Image: "img:1", Dest: "/d", Slug: "s", BaseDir: "/b",
		Profile: &config.Profile{Env: map[string]string{"B": "2", "A": "1"}},
	}
	h := ConfigHash(base, "", "")
	if len(h) != 64 || strings.Trim(h, "0123456789abcdef") != "" {
		t.Fatalf("ConfigHash = %q, want 64 hex chars", h)
	}
	// Stable across calls (multi-key profile env exercises the sorted order).
	for i := 0; i < 5; i++ {
		if g := ConfigHash(base, "", ""); g != h {
			t.Fatalf("ConfigHash unstable: %q vs %q", g, h)
		}
	}

	// Name/labels are excluded: BaseDir feeds only the name + labels, and Wall
	// is deliberately ignored — neither may flip the hash.
	same := []struct {
		desc string
		o    RunOpts
	}{
		{"different BaseDir", func() RunOpts { o := base; o.BaseDir = "/elsewhere"; return o }()},
		{"wall timeout set", func() RunOpts { o := base; o.Wall = "60"; return o }()},
	}
	for _, tc := range same {
		if g := ConfigHash(tc.o, "", ""); g != h {
			t.Errorf("%s changed the hash: %q vs %q", tc.desc, g, h)
		}
	}

	// Any real config change flips it.
	diff := []struct {
		desc string
		o    RunOpts
		eg   [2]string
	}{
		{"image", func() RunOpts { o := base; o.Image = "img:2"; return o }(), [2]string{"", ""}},
		{"memory limit", func() RunOpts { o := base; o.Mem = "2G"; return o }(), [2]string{"", ""}},
		{"profile env", func() RunOpts {
			o := base
			o.Profile = &config.Profile{Env: map[string]string{"B": "2", "A": "changed"}}
			return o
		}(), [2]string{"", ""}},
		{"extra mount", func() RunOpts {
			o := base
			o.Profile = &config.Profile{
				Env:         map[string]string{"B": "2", "A": "1"},
				ExtraMounts: []config.Mount{{Source: "/s", Target: "/t"}},
			}
			return o
		}(), [2]string{"", ""}},
		{"egress network", base, [2]string{"net", "http://proxy"}},
	}
	for _, tc := range diff {
		if g := ConfigHash(tc.o, tc.eg[0], tc.eg[1]); g == h {
			t.Errorf("%s did not change the hash: %q", tc.desc, g)
		}
	}
}
