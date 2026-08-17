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
