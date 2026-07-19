package egress

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
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
	if _, err := Up("podman", "slug", nil, "", nil, "", io.Discard); err == nil {
		t.Error("Up with no domains should error")
	}
	if _, err := Up("podman", "slug", []string{}, "", nil, "", io.Discard); err == nil {
		t.Error("Up with empty domains should error")
	}
}

// TestSquidConfRejectsInjection: even if a domain carrying a newline/tab slipped
// past config validation, writeDomains must not emit it — squid.conf can never
// gain a stray directive from allowlist data. The legitimate line is
// `http_access allow allowed`, so we look for the injected `http_access allow
// all` as a line of its own.
func TestSquidConfRejectsInjection(t *testing.T) {
	c := squidConf([]string{"a.com\nhttp_access allow all", "\tb.evil\tallow", "good.com"}, "", nil)
	for _, line := range strings.Split(c, "\n") {
		if strings.TrimSpace(line) == "http_access allow all" {
			t.Fatalf("injected directive reached squid.conf:\n%s", c)
		}
	}
	// The clean entry still lands; the tainted ones are dropped.
	if !strings.Contains(c, "acl allowed dstdomain .good.com\n") {
		t.Errorf("clean domain missing / tainted domains not dropped:\n%s", c)
	}
}

// TestSquidConf pins the generated allowlist config: each domain becomes a
// leading-dot dstdomain (host + subdomains), HTTP and CONNECT-to-443 for allowed
// domains are permitted, everything else denied; a non-empty upstream chains via
// cache_peer.
func TestSquidConf(t *testing.T) {
	c := squidConf([]string{"a.com", ".b.com"}, "", nil)
	for _, want := range []string{
		"http_port 8888",
		"acl allowed dstdomain .a.com .b.com",
		"http_access allow allowed",
		"http_access deny all",
		"http_access deny CONNECT !SSL_ports",
	} {
		if !strings.Contains(c, want) {
			t.Errorf("squidConf missing %q in:\n%s", want, c)
		}
	}
	if strings.Contains(c, "cache_peer") {
		t.Errorf("no upstream should mean no cache_peer:\n%s", c)
	}
	withUp := squidConf([]string{"a.com"}, "http://proxy.host:3128", nil)
	if !strings.Contains(withUp, "cache_peer proxy.host parent 3128") || !strings.Contains(withUp, "never_direct allow all") {
		t.Errorf("upstream should produce a cache_peer line:\n%s", withUp)
	}
}

// TestSquidConfRoutes pins the per-domain routing config: a routed domain gets
// its own acl + named cache_peer, is forced through it (never_direct), and is
// excluded from the default peer; a default upstream still serves everything
// else.
func TestSquidConfRoutes(t *testing.T) {
	routes := []config.Route{{Domains: []string{"api.geo.com"}, Proxy: "http://bypass:8080"}}
	c := squidConf([]string{"a.com", "api.geo.com"}, "http://corp:3128", routes)
	for _, want := range []string{
		"acl sbxroute0 dstdomain .api.geo.com",
		"cache_peer bypass parent 8080 0 no-query name=sbxpeer0",
		"cache_peer_access sbxpeer0 allow sbxroute0",
		"cache_peer_access sbxpeer0 deny all",
		"never_direct allow sbxroute0",
		"cache_peer corp parent 3128 0 no-query default name=sbxdefault",
		"cache_peer_access sbxdefault deny sbxroute0",
		"cache_peer_access sbxdefault allow all",
	} {
		if !strings.Contains(c, want) {
			t.Errorf("routed squidConf missing %q in:\n%s", want, c)
		}
	}

	// Routes with no default upstream: the routed peer is present but unrouted
	// traffic is left to go direct (no default peer, no blanket never_direct).
	d := squidConf([]string{"a.com", "api.geo.com"}, "", routes)
	if !strings.Contains(d, "cache_peer bypass parent 8080 0 no-query name=sbxpeer0") {
		t.Errorf("route peer missing without a default upstream:\n%s", d)
	}
	if strings.Contains(d, "never_direct allow all") || strings.Contains(d, "default name=sbxdefault") {
		t.Errorf("no default upstream should mean no default peer / blanket never_direct:\n%s", d)
	}
}

// TestConfFingerprint: the fingerprint tracks the squid.conf content — equal for
// equal inputs, different when the routes (or domains/upstream) change.
func TestConfFingerprint(t *testing.T) {
	domains := []string{"a.com", "api.geo.com"}
	routes := []config.Route{{Domains: []string{"api.geo.com"}, Proxy: "http://bypass:8080"}}
	base := ConfFingerprint(domains, "", nil)
	if base != ConfFingerprint(domains, "", nil) {
		t.Error("ConfFingerprint must be stable for equal inputs")
	}
	if base == ConfFingerprint(domains, "", routes) {
		t.Error("adding a route must change the fingerprint")
	}
	if base == ConfFingerprint(domains, "http://corp:3128", nil) {
		t.Error("adding an upstream must change the fingerprint")
	}
	if len(base) != 64 {
		t.Errorf("fingerprint = %q, want 64 hex chars", base)
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

	confDir := t.TempDir()
	e, err := Up(engine, "my/slug", []string{"a.com", "b.com"}, "", nil, confDir, io.Discard)
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
	// The squid.conf was written with the allowlist.
	conf, err := os.ReadFile(e.conf)
	if err != nil || !strings.Contains(string(conf), ".a.com") || !strings.Contains(string(conf), "http_access deny all") {
		t.Errorf("squid.conf not written with the allowlist: %q err=%v", conf, err)
	}

	log := readLog(t, logPath)
	for _, want := range []string{
		"network create --internal",
		"run -d --name",
		"--cap-drop=ALL",
		"/etc/sandboxer/squid.conf:ro",
		"sandboxer-proxy",
		"network connect",
	} {
		if !strings.Contains(log, want) {
			t.Errorf("engine log missing %q:\n%s", want, log)
		}
	}

	e.Down()
	// Down removes the generated config.
	if _, err := os.Stat(e.conf); !os.IsNotExist(err) {
		t.Errorf("Down should remove the squid.conf (err=%v)", err)
	}
	if e.Active() {
		t.Error("Egress should be inactive after Down")
	}
	log = readLog(t, logPath)
	if !strings.Contains(log, "rm -f") || !strings.Contains(log, "network rm") {
		t.Errorf("Down did not invoke teardown:\n%s", log)
	}
}

func TestUpWithUpstream(t *testing.T) {
	engine, _ := fakeEngine(t)
	t.Setenv("SBX_FAIL_ON", "")

	e, err := Up(engine, "slug", []string{"a.com"}, "http://host.docker.internal:3128", nil, t.TempDir(), io.Discard)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	t.Cleanup(e.Down)

	// The upstream is chained via a squid cache_peer in the generated config.
	conf, _ := os.ReadFile(e.conf)
	if !strings.Contains(string(conf), "cache_peer host.docker.internal parent 3128") {
		t.Errorf("squid.conf missing upstream cache_peer:\n%s", conf)
	}
}

func TestUpProxyFailureTearsDown(t *testing.T) {
	engine, logPath := fakeEngine(t)
	// Networks succeed; the proxy `run` fails, forcing Up to tear down.
	t.Setenv("SBX_FAIL_ON", "run")

	e, err := Up(engine, "slug", []string{"a.com"}, "", nil, t.TempDir(), io.Discard)
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

	e, err := Up(engine, "slug", []string{"a.com"}, "", nil, t.TempDir(), io.Discard)
	if err == nil || e != nil {
		t.Errorf("Up should fail and return nil when network creation fails (err=%v e=%v)", err, e)
	}
}

func TestUpNamedStableNamesAndPreClean(t *testing.T) {
	engine, logPath := fakeEngine(t)
	t.Setenv("SBX_FAIL_ON", "")

	e, err := UpNamed(engine, "sbx-mysess", []string{"a.com"}, "", nil, t.TempDir(), io.Discard)
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

	if _, err := UpNamed(engine, "sbx-x", nil, "", nil, "", io.Discard); err == nil {
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
