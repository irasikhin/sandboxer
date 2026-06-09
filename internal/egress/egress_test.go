package egress

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeEngine writes an executable shell stub that appends each invocation's
// argv to a log file and exits 1 whenever any argument equals $SBX_FAIL_ON
// (empty = always succeed). It returns the engine path and the log path.
func fakeEngine(t *testing.T) (engine, logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	engine = filepath.Join(dir, "engine")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> \"" + logPath + "\"\n" +
		"for a in \"$@\"; do [ \"$a\" = \"$SBX_FAIL_ON\" ] && [ -n \"$SBX_FAIL_ON\" ] && exit 1; done\n" +
		"exit 0\n"
	if err := os.WriteFile(engine, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return engine, logPath
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func TestUpNoDomains(t *testing.T) {
	if _, err := Up("podman", "img", "slug", nil, "", io.Discard); err == nil {
		t.Error("Up with no domains should error")
	}
	if _, err := Up("podman", "img", "slug", []string{}, "", io.Discard); err == nil {
		t.Error("Up with empty domains should error")
	}
}

func TestNilSafety(t *testing.T) {
	var e *Egress
	if e.Active() {
		t.Error("nil Egress should not be active")
	}
	e.Down() // must not panic
}

func TestGetters(t *testing.T) {
	e := &Egress{net: "n-int", proxy: "n-proxy"}
	if e.Net() != "n-int" {
		t.Errorf("Net = %q", e.Net())
	}
	if e.ProxyURL() != "http://n-proxy:8888" {
		t.Errorf("ProxyURL = %q", e.ProxyURL())
	}
	if e.Active() {
		t.Error("freshly constructed Egress should not report active")
	}
}

func TestUpDownSuccess(t *testing.T) {
	engine, logPath := fakeEngine(t)
	t.Setenv("SBX_FAIL_ON", "")

	e, err := Up(engine, "toolbox:latest", "my/slug", []string{"a.com", "b.com"}, "", io.Discard)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if !e.Active() {
		t.Error("Egress should be active after a successful Up")
	}
	// Slug is sanitized into the resource names.
	if !strings.Contains(e.Net(), "my-slug") {
		t.Errorf("net name %q missing sanitized slug", e.Net())
	}
	if !strings.HasSuffix(e.Net(), "-int") {
		t.Errorf("internal net %q should end with -int", e.Net())
	}

	log := readLog(t, logPath)
	for _, want := range []string{
		"network create --internal",
		"run -d --name",
		"--cap-drop=ALL",
		"--allow a.com",
		"--allow b.com",
		"network connect",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("engine log missing %q:\n%s", want, log)
		}
	}

	e.Down()
	if e.Active() {
		t.Error("Egress should be inactive after Down")
	}
	log = readLog(t, logPath)
	if !strings.Contains(log, "rm -f") || !strings.Contains(log, "network rm") {
		t.Errorf("Down did not invoke teardown:\n%s", log)
	}
}

func TestUpWithUpstream(t *testing.T) {
	engine, logPath := fakeEngine(t)
	t.Setenv("SBX_FAIL_ON", "")

	e, err := Up(engine, "toolbox:latest", "slug", []string{"a.com"}, "http://host.docker.internal:3128", io.Discard)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	t.Cleanup(e.Down)

	if log := readLog(t, logPath); !strings.Contains(log, "--upstream http://host.docker.internal:3128") {
		t.Errorf("sidecar argv missing --upstream:\n%s", log)
	}
}

func TestUpProxyFailureTearsDown(t *testing.T) {
	engine, logPath := fakeEngine(t)
	// Networks succeed; the proxy `run` fails, forcing Up to tear down.
	t.Setenv("SBX_FAIL_ON", "run")

	e, err := Up(engine, "img", "slug", []string{"a.com"}, "", io.Discard)
	if err == nil {
		t.Fatal("Up should fail when the proxy sidecar cannot start")
	}
	if e != nil {
		t.Error("Up should return a nil Egress on failure")
	}
	// The teardown removed the networks it had created.
	if log := readLog(t, logPath); !strings.Contains(log, "network rm") {
		t.Errorf("failed Up did not clean up networks:\n%s", log)
	}
}

func TestUpNetworkFailure(t *testing.T) {
	engine, _ := fakeEngine(t)
	t.Setenv("SBX_FAIL_ON", "network") // first `network create` fails

	e, err := Up(engine, "img", "slug", []string{"a.com"}, "", io.Discard)
	if err == nil || e != nil {
		t.Errorf("Up should fail and return nil when network creation fails (err=%v e=%v)", err, e)
	}
}
