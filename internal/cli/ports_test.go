package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
)

// TestCreatePortFlag: --port publishes a forward and the banner names the URL
// to open, so the one thing a user needs after publishing is on screen.
func TestCreatePortFlag(t *testing.T) {
	project := newProject(t)
	fakeMsb(t)
	code, _, errs := run("create", "feat", "--src", project, "--port", "8080:3080")
	if code != 0 {
		t.Fatalf("create --port = %d, %s", code, errs)
	}
	if !strings.Contains(errs, "port 127.0.0.1:8080→3080/tcp") || !strings.Contains(errs, "http://127.0.0.1:8080/") {
		t.Errorf("create --port should report the forward: %q", errs)
	}
}

// TestCreatePortFlagInvalid: a malformed spec fails the command outright —
// create must not reach the state-writing half with a port it cannot publish.
func TestCreatePortFlagInvalid(t *testing.T) {
	project := newProject(t)
	fakeMsb(t)
	code, _, errs := run("create", "feat", "--src", project, "--port", "8080:nope")
	if code != 1 || !strings.Contains(errs, "invalid port") {
		t.Errorf("create --port 8080:nope = (%d, %q), want the parse error", code, errs)
	}
}

// TestReportPorts pins the banner: every forward is named, and a non-loopback
// bind — the one that puts a guest port on the network — is a WARNING.
func TestReportPorts(t *testing.T) {
	var b bytes.Buffer
	reportPorts(&b, config.Runtime{})
	if b.Len() != 0 {
		t.Errorf("no ports should print nothing, got %q", b.String())
	}

	b.Reset()
	reportPorts(&b, config.Runtime{Ports: []config.Port{
		{Bind: "127.0.0.1", Host: 8080, Guest: 3080, Proto: "tcp"},
	}})
	if out := b.String(); !strings.Contains(out, "port 127.0.0.1:8080→3080/tcp") || strings.Contains(out, "WARNING") {
		t.Errorf("loopback forward = %q, want a plain line", out)
	}

	b.Reset()
	reportPorts(&b, config.Runtime{Ports: []config.Port{
		{Bind: "0.0.0.0", Host: 8080, Guest: 3080, Proto: "tcp"},
	}})
	if out := b.String(); !strings.Contains(out, "WARNING") || !strings.Contains(out, "NON-loopback") {
		t.Errorf("public forward = %q, want the exposure warning", out)
	}
}

// TestNoPortsKillSwitch: SANDBOXER_NO_PORTS=1 closes every forward, whatever
// the profile or the flag says — the inbound counterpart of
// SANDBOXER_NO_EGRESS.
func TestNoPortsKillSwitch(t *testing.T) {
	project := newProject(t)
	fakeMsb(t)
	t.Setenv("SANDBOXER_NO_PORTS", "1")
	code, _, errs := run("create", "feat", "--src", project, "--port", "8080:3080")
	if code != 0 {
		t.Fatalf("create = %d, %s", code, errs)
	}
	if strings.Contains(errs, "port 127.0.0.1:8080") {
		t.Errorf("SANDBOXER_NO_PORTS=1 must drop the forward: %q", errs)
	}
}

// TestVerifyPorts: the message that keeps enter honest. It reports what the
// MACHINE publishes, so a session created before the port existed is caught as
// a fact rather than inferred from staleness — and an unreachable runner stays
// silent instead of crying wolf.
func TestVerifyPorts(t *testing.T) {
	p3080 := config.Port{Bind: "127.0.0.1", Host: 3080, Guest: 3080, Proto: "tcp"}
	rt := config.Runtime{Ports: []config.Port{p3080}}

	restore := backendSessionPorts
	t.Cleanup(func() { backendSessionPorts = restore })

	var b bytes.Buffer
	backendSessionPorts = func(_, _ string) []config.Port { return []config.Port{p3080} }
	verifyPorts(&b, rt, "feat", "microsandbox", "m")
	if b.Len() != 0 {
		t.Errorf("published forward must print nothing, got %q", b.String())
	}

	b.Reset()
	backendSessionPorts = func(_, _ string) []config.Port { return []config.Port{} }
	verifyPorts(&b, rt, "feat", "microsandbox", "m")
	out := b.String()
	for _, want := range []string{"WARNING", "does NOT publish", "127.0.0.1:3080→3080/tcp", "sandboxer stop feat"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing forward warning = %q, want it to mention %q", out, want)
		}
	}

	// Unknown (no runner, unreadable output) is not evidence of a missing
	// forward, and no configured port means nothing to check.
	b.Reset()
	backendSessionPorts = func(_, _ string) []config.Port { return nil }
	verifyPorts(&b, rt, "feat", "microsandbox", "m")
	verifyPorts(&b, config.Runtime{}, "feat", "microsandbox", "m")
	if b.Len() != 0 {
		t.Errorf("unknown/none must stay silent, got %q", b.String())
	}
}

// TestPortsBlock pins show's ports section: it reports what the RUNNING
// machine publishes, not what the config wishes for — the difference is the
// whole reason the block exists.
func TestPortsBlock(t *testing.T) {
	fresh, stale := true, false
	p8080 := config.Port{Bind: "127.0.0.1", Host: 8080, Guest: 3080, Proto: "tcp"}
	ports := config.Runtime{Ports: []config.Port{p8080}}

	var b bytes.Buffer
	printPortsBlock(&b, config.Runtime{}, showSession{State: "running", Fresh: &fresh}, nil)
	if !strings.Contains(b.String(), "(none") {
		t.Errorf("no ports = %q, want the empty note", b.String())
	}

	b.Reset()
	printPortsBlock(&b, ports, showSession{State: "running", Fresh: &fresh}, []config.Port{p8080})
	if out := b.String(); !strings.Contains(out, "open http://127.0.0.1:8080/") || strings.Contains(out, "NOT published") {
		t.Errorf("published forward = %q, want the URL", out)
	}

	// The case that sent a user to a browser error page: the machine runs and
	// reads fresh, but it was created without the forward.
	b.Reset()
	printPortsBlock(&b, ports, showSession{State: "running", Fresh: &fresh}, nil)
	if out := b.String(); !strings.Contains(out, "NOT published by the running machine") ||
		!strings.Contains(out, "created without it") {
		t.Errorf("missing forward = %q, want the rebuild hint", out)
	}

	b.Reset()
	printPortsBlock(&b, ports, showSession{State: "running", Fresh: &stale, StaleWhy: "profile changed"}, nil)
	if out := b.String(); !strings.Contains(out, "stale") || !strings.Contains(out, "profile changed") {
		t.Errorf("stale session = %q, want the reason", out)
	}

	b.Reset()
	printPortsBlock(&b, ports, showSession{State: "none"}, nil)
	if out := b.String(); !strings.Contains(out, "no session machine yet") {
		t.Errorf("missing session = %q, want the create hint", out)
	}

	// A forward the machine still holds after the config dropped it explains a
	// host port that looks taken by nobody.
	b.Reset()
	printPortsBlock(&b, config.Runtime{}, showSession{State: "running", Fresh: &fresh}, []config.Port{p8080})
	if out := b.String(); !strings.Contains(out, "no longer in the config") {
		t.Errorf("leftover forward = %q, want it named", out)
	}

	// The JSON projection carries the same verdict.
	if got := showPorts(ports, []config.Port{p8080}); len(got) != 1 || !got[0].Live || got[0].URL != "http://127.0.0.1:8080/" {
		t.Errorf("showPorts (published) = %+v", got)
	}
	if got := showPorts(ports, nil); len(got) != 1 || got[0].Live {
		t.Errorf("showPorts (absent) = %+v", got)
	}
}

// TestLivePorts: the runner is asked only about a machine that is running —
// anything else forwards nothing by definition.
func TestLivePorts(t *testing.T) {
	called := 0
	restore := backendSessionPorts
	backendSessionPorts = func(_, _ string) []config.Port {
		called++
		return []config.Port{{Bind: "127.0.0.1", Host: 8080, Guest: 3080, Proto: "tcp"}}
	}
	t.Cleanup(func() { backendSessionPorts = restore })

	if got := livePorts(config.Runtime{}, showSession{State: "stopped"}); got != nil || called != 0 {
		t.Errorf("stopped session = %+v (runner called %d times), want none", got, called)
	}
	if got := livePorts(config.Runtime{}, showSession{State: "running", Container: "m"}); len(got) != 1 || called != 1 {
		t.Errorf("running session = %+v (runner called %d times)", got, called)
	}
}

// TestValidateProfileUsesEnvDefaults: `config validate` judges a profile the
// way the commands that RUN it do. An omitted `backend` is normal — it resolves
// to the SANDBOXER_BACKEND default — so validate must not report it as the
// retired container backend, which is what judging against a zero Defaults did.
func TestValidateProfileUsesEnvDefaults(t *testing.T) {
	p := config.Profile{Srcs: []config.Src{{Src: ".", Branch: "feat/x"}}, Ports: []string{"3080"}}
	if err := validateProfile(p); err != nil {
		t.Fatalf("validateProfile(no backend) = %v, want nil", err)
	}
	// A retired value the user DID write is still an error.
	p.Backend = "docker"
	if err := validateProfile(p); err == nil {
		t.Error("validateProfile(backend=docker) = nil, want the migration error")
	}
	// And a malformed port is caught here too, before anything is created.
	bad := config.Profile{Srcs: []config.Src{{Src: ".", Branch: "feat/x"}}, Ports: []string{"3080:nope"}}
	if err := validateProfile(bad); err == nil {
		t.Error("validateProfile(bad port) = nil, want the parse error")
	}
}
