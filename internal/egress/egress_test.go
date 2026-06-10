package egress

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeEngine writes an executable shell stub that appends each invocation's
// argv to a log file, exits 1 whenever any argument equals $SBX_FAIL_ON
// (empty = always succeed), and prints $SBX_STDOUT plus a newline when set
// (mimicking e.g. `inspect --format` output). It returns the engine path and
// the log path.
func fakeEngine(t *testing.T) (engine, logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	engine = filepath.Join(dir, "engine")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> \"" + logPath + "\"\n" +
		"for a in \"$@\"; do [ \"$a\" = \"$SBX_FAIL_ON\" ] && [ -n \"$SBX_FAIL_ON\" ] && exit 1; done\n" +
		"[ -n \"$SBX_STDOUT\" ] && printf '%s\\n' \"$SBX_STDOUT\"\n" +
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
	if e.ProxyRunning() {
		t.Error("nil Egress should not report a running proxy")
	}
	if err := e.Stop(); err != nil {
		t.Errorf("nil Egress Stop should be a no-op, got %v", err)
	}
	if err := e.Start(); err != nil {
		t.Errorf("nil Egress Start should be a no-op, got %v", err)
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

func TestUpNamedStableNamesAndPreClean(t *testing.T) {
	engine, logPath := fakeEngine(t)
	t.Setenv("SBX_FAIL_ON", "")

	e, err := UpNamed(engine, "toolbox:latest", "sbx-mysess", []string{"a.com"}, "", io.Discard)
	if err != nil {
		t.Fatalf("UpNamed: %v", err)
	}
	if !e.Active() {
		t.Error("Egress should be active after a successful UpNamed")
	}
	// Names derive from the id verbatim — no PID — so a later CLI invocation
	// can reconstruct them via Lookup.
	if e.Net() != "sbx-mysess-int" {
		t.Errorf("net name = %q, want sbx-mysess-int", e.Net())
	}
	if e.ProxyURL() != "http://sbx-mysess-proxy:8888" {
		t.Errorf("ProxyURL = %q", e.ProxyURL())
	}

	log := readLog(t, logPath)
	// The pre-clean (Down semantics on all three resources) runs before any
	// create, so leftovers from a previous life cannot collide.
	firstCreate := strings.Index(log, "network create")
	if firstCreate == -1 {
		t.Fatalf("engine log missing network create:\n%s", log)
	}
	for _, want := range []string{
		"rm -f sbx-mysess-proxy",
		"network rm sbx-mysess-int",
		"network rm sbx-mysess-ext",
	} {
		i := strings.Index(log, want)
		if i == -1 {
			t.Errorf("engine log missing pre-clean %q:\n%s", want, log)
		} else if i > firstCreate {
			t.Errorf("pre-clean %q ran after create:\n%s", want, log)
		}
	}
	if !strings.Contains(log, "run -d --name sbx-mysess-proxy") {
		t.Errorf("engine log missing stable-named proxy run:\n%s", log)
	}
}

func TestUpNamedNoDomains(t *testing.T) {
	engine, logPath := fakeEngine(t)
	t.Setenv("SBX_FAIL_ON", "")

	if _, err := UpNamed(engine, "img", "sbx-x", nil, "", io.Discard); err == nil {
		t.Error("UpNamed with no domains should error")
	}
	// Validation precedes the pre-clean: nothing was removed or created.
	if log := readLog(t, logPath); log != "" {
		t.Errorf("UpNamed with no domains should not touch the engine:\n%s", log)
	}
}

func TestLookupDown(t *testing.T) {
	engine, logPath := fakeEngine(t)
	t.Setenv("SBX_FAIL_ON", "")

	e := Lookup(engine, "sbx-mysess")
	if e.Active() {
		t.Error("Lookup handle should not report active")
	}
	e.Down()

	log := readLog(t, logPath)
	for _, want := range []string{
		"rm -f sbx-mysess-proxy",
		"network rm sbx-mysess-int",
		"network rm sbx-mysess-ext",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("Lookup+Down missing %q:\n%s", want, log)
		}
	}
}

func TestStopStartProxyOnly(t *testing.T) {
	engine, logPath := fakeEngine(t)
	t.Setenv("SBX_FAIL_ON", "")

	e := Lookup(engine, "sbx-mysess")
	if err := e.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := e.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Exactly two engine calls, both addressing only the proxy container —
	// the networks persist across Stop/Start.
	got := strings.Split(strings.TrimSpace(readLog(t, logPath)), "\n")
	want := []string{"stop sbx-mysess-proxy", "start sbx-mysess-proxy"}
	if len(got) != len(want) {
		t.Fatalf("engine calls = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("engine call %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestStopStartErrors(t *testing.T) {
	engine, _ := fakeEngine(t)
	e := Lookup(engine, "sbx-mysess")

	t.Setenv("SBX_FAIL_ON", "stop")
	if err := e.Stop(); err == nil {
		t.Error("Stop should surface the engine failure")
	}
	t.Setenv("SBX_FAIL_ON", "start")
	if err := e.Start(); err == nil {
		t.Error("Start should surface the engine failure")
	}
}

func TestProxyRunning(t *testing.T) {
	engine, logPath := fakeEngine(t)
	e := Lookup(engine, "sbx-mysess")

	tests := []struct {
		name   string
		stdout string
		failOn string
		want   bool
	}{
		{"running", "true", "", true},
		{"stopped", "false", "", false},
		{"no such container", "", "inspect", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SBX_STDOUT", tc.stdout)
			t.Setenv("SBX_FAIL_ON", tc.failOn)
			if got := e.ProxyRunning(); got != tc.want {
				t.Errorf("ProxyRunning = %v, want %v", got, tc.want)
			}
		})
	}

	want := "container inspect --format {{.State.Running}} sbx-mysess-proxy"
	if log := readLog(t, logPath); !strings.Contains(log, want) {
		t.Errorf("engine log missing %q:\n%s", want, log)
	}
}
