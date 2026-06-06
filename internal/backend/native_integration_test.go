//go:build integration

package backend

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/itest"
)

// TestNativeExec_RealClaudeWrapping_StubBinary runs NativeExec against a stub
// `claude` on PATH and asserts the REAL argv/cwd/env it received — observed from
// the spawned process, not a fake: the native-sandbox --settings (egress
// allowlist), --model, the trailing user args, the working dir (= dest) and the
// exported proxy environment.
func TestNativeExec_RealClaudeWrapping_StubBinary(t *testing.T) {
	requireExec(t, "sh")
	bin := t.TempDir()
	dumps := t.TempDir()
	argDump := filepath.Join(dumps, "args")
	envDump := filepath.Join(dumps, "env")
	itest.WriteStub(t, bin, "claude",
		"printf '%s\\n' \"$@\" > '"+argDump+"'\n"+
			"{ echo \"PWD=$PWD\"; echo \"HTTPS_PROXY=$HTTPS_PROXY\"; } > '"+envDump+"'\n"+
			"exit 0\n")
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))

	dest := t.TempDir()
	rt := config.Runtime{Model: "claude-x", Domains: []string{"api.anthropic.com"}, HTTPSProxy: "http://proxy:8888"}
	var out, errb bytes.Buffer
	code, err := NativeExec(dest, rt, []string{"claude", "-p", "hi"}, strings.NewReader(""), &out, &errb)
	if err != nil || code != 0 {
		t.Fatalf("NativeExec = (%d, %v)\n%s", code, err, errb.String())
	}

	args, _ := os.ReadFile(argDump)
	as := string(args)
	for _, want := range []string{"--settings", "allowedDomains", "api.anthropic.com", "--model", "claude-x", "-p", "hi"} {
		if !strings.Contains(as, want) {
			t.Errorf("claude argv missing %q:\n%s", want, as)
		}
	}
	env, _ := os.ReadFile(envDump)
	es := string(env)
	if !strings.Contains(es, "PWD="+dest) {
		t.Errorf("claude cwd = %q, want PWD=%s", es, dest)
	}
	if !strings.Contains(es, "HTTPS_PROXY=http://proxy:8888") {
		t.Errorf("proxy env not exported to claude:\n%s", es)
	}
}

// TestNativeEnter_RealShell_ProxyEnv runs NativeEnter with $SHELL set to a stub
// that records its cwd and proxy env, proving the real shell is launched in the
// sandbox dir with the proxy environment applied.
func TestNativeEnter_RealShell_ProxyEnv(t *testing.T) {
	requireExec(t, "sh")
	bin := t.TempDir()
	dump := filepath.Join(t.TempDir(), "out")
	stub := itest.WriteStub(t, bin, "shellstub",
		"{ echo \"PWD=$PWD\"; echo \"HTTP_PROXY=$HTTP_PROXY\"; } > '"+dump+"'\n")
	t.Setenv("SHELL", stub)

	dest := t.TempDir()
	rt := config.Runtime{HTTPProxy: "http://proxy:3128"}
	if err := NativeEnter(dest, rt, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("NativeEnter: %v", err)
	}
	s, _ := os.ReadFile(dump)
	out := string(s)
	if !strings.Contains(out, "PWD="+dest) {
		t.Errorf("shell cwd = %q, want PWD=%s", out, dest)
	}
	if !strings.Contains(out, "HTTP_PROXY=http://proxy:3128") {
		t.Errorf("proxy env not exported to shell:\n%s", out)
	}
}
