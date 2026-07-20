// Package execx runs external commands so a failure carries the command's own
// stderr, not just its exit status.
//
// The engine (podman/docker) writes the only line that explains a failure —
// "cannot set up namespace", "network name already in use" — to stderr. A bare
// exec.Command(...).Run() discards it, leaving the caller's wrapped error
// ending in "exit status 125" and the user with nothing to act on. Every
// non-interactive engine call goes through here instead; the interactive ones
// (backend.Run, backend.ExecSession) already wire stderr to the user's
// terminal and must not be routed here.
package execx

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// stderrTailLines and stderrTailBytes bound the diagnostic appended to an
// error. Podman prints benign warnings (missing subuid range, non-shared /) on
// EVERY invocation, so the real error is at the tail — keep that end, not the
// head, and keep the whole thing short enough to read.
const (
	stderrTailLines = 10
	stderrTailBytes = 2 << 10
)

// Run runs bin with args, discarding stdout. On failure the returned error
// wraps the exec error and appends the command's stderr, so a caller's
// fmt.Errorf("…: %w", err) message ends with the tool's own diagnostic. The
// *exec.ExitError stays reachable through errors.As.
func Run(bin string, args ...string) error {
	cmd := exec.Command(bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return withStderr(err, stderr.Bytes())
	}
	return nil
}

// Output runs bin with args and returns its stdout. On failure the error
// carries stderr the same way Run's does — exec.Cmd.Output captures it into
// ExitError.Stderr because Stderr is left nil.
func Output(bin string, args ...string) (string, error) {
	out, err := exec.Command(bin, args...).Output()
	if err != nil {
		var stderr []byte
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr = ee.Stderr
		}
		return "", withStderr(err, stderr)
	}
	return string(out), nil
}

// withStderr appends the command's diagnostic to err, or returns err unchanged
// when the command said nothing — a silent failure keeps its plain
// "exit status N" rather than gaining an empty suffix.
func withStderr(err error, stderr []byte) error {
	msg := tail(string(stderr))
	if msg == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, msg)
}

// tail trims s to its last few meaningful lines, joined with "; " so the whole
// diagnostic stays on one line beside the caller's wrapping context. Blank
// lines are dropped; the byte cap is applied last, keeping the end.
func tail(s string) string {
	var kept []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			kept = append(kept, line)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	if len(kept) > stderrTailLines {
		kept = kept[len(kept)-stderrTailLines:]
	}
	msg := strings.Join(kept, "; ")
	if len(msg) > stderrTailBytes {
		msg = "…" + msg[len(msg)-stderrTailBytes:]
	}
	return msg
}
