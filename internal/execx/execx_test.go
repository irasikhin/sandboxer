package execx

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// script writes an executable /bin/sh stub and returns its path. body runs with
// no arguments of its own; tests use it to control stdout, stderr and the exit
// code independently.
func script(t *testing.T, body string) string {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	path := filepath.Join(t.TempDir(), "stub")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunSuccess(t *testing.T) {
	// Stderr on a SUCCESSFUL run is dropped, not surfaced: podman writes its
	// benign rootless warnings there on every invocation.
	if err := Run(script(t, `echo "WARN: not a shared mount" >&2; exit 0`)); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
}

func TestRunFailureCarriesStderr(t *testing.T) {
	err := Run(script(t, `echo "Error: cannot set up namespace" >&2; exit 125`))
	if err == nil {
		t.Fatal("Run = nil, want an error")
	}
	if !strings.Contains(err.Error(), "cannot set up namespace") {
		t.Errorf("error lost the engine's diagnostic: %v", err)
	}
	// The exit status must survive alongside it — exitCode() and any errors.As
	// caller still need the *exec.ExitError.
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("error no longer unwraps to *exec.ExitError: %v", err)
	}
	if ee.ExitCode() != 125 {
		t.Errorf("ExitCode = %d, want 125", ee.ExitCode())
	}
}

func TestRunSilentFailureKeepsBareStatus(t *testing.T) {
	err := Run(script(t, "exit 7"))
	if err == nil {
		t.Fatal("Run = nil, want an error")
	}
	if got := err.Error(); got != "exit status 7" {
		t.Errorf("Run error = %q, want a bare %q", got, "exit status 7")
	}
}

func TestRunMissingBinary(t *testing.T) {
	// A binary that does not exist never runs, so there is no stderr to add.
	if err := Run(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("Run = nil, want an error")
	}
}

func TestOutput(t *testing.T) {
	out, err := Output(script(t, `echo hello; echo noise >&2`))
	if err != nil {
		t.Fatalf("Output = %v", err)
	}
	if strings.TrimSpace(out) != "hello" {
		t.Errorf("Output = %q, want %q", out, "hello")
	}
}

func TestOutputFailureCarriesStderr(t *testing.T) {
	out, err := Output(script(t, `echo partial; echo "Error: no such container" >&2; exit 125`))
	if err == nil {
		t.Fatal("Output = nil, want an error")
	}
	if out != "" {
		t.Errorf("Output = %q on failure, want empty", out)
	}
	if !strings.Contains(err.Error(), "no such container") {
		t.Errorf("error lost the engine's diagnostic: %v", err)
	}
}

func TestTail(t *testing.T) {
	cases := map[string]string{
		"":                    "",
		"   \n\n  ":           "",
		"one":                 "one",
		"  one  \n":           "one",
		"one\n\ntwo\n":        "one; two",
		"WARN: a\nError: b\n": "WARN: a; Error: b",
	}
	for in, want := range cases {
		if got := tail(in); got != want {
			t.Errorf("tail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTailKeepsTheEnd(t *testing.T) {
	// Podman's benign warnings come FIRST and the real error LAST, so an
	// over-long diagnostic must be trimmed from the head.
	var lines []string
	for i := range stderrTailLines + 5 {
		lines = append(lines, "line"+string(rune('a'+i)))
	}
	lines = append(lines, "Error: the real one")
	got := tail(strings.Join(lines, "\n"))
	if !strings.HasSuffix(got, "Error: the real one") {
		t.Errorf("tail dropped the last line: %q", got)
	}
	if strings.Contains(got, "linea") {
		t.Errorf("tail kept the head instead of the tail: %q", got)
	}
	if n := strings.Count(got, "; ") + 1; n != stderrTailLines {
		t.Errorf("tail kept %d lines, want %d", n, stderrTailLines)
	}
}

func TestTailByteCap(t *testing.T) {
	long := strings.Repeat("x", stderrTailBytes*2) + "TAIL"
	got := tail(long)
	if len(got) > stderrTailBytes+len("…") {
		t.Errorf("tail returned %d bytes, want <= %d", len(got), stderrTailBytes)
	}
	if !strings.HasSuffix(got, "TAIL") {
		t.Errorf("byte cap dropped the end: %q", got[len(got)-20:])
	}
	if !strings.HasPrefix(got, "…") {
		t.Errorf("truncation is not marked: %q", got[:20])
	}
}
