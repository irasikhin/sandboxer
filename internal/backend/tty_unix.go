//go:build unix

package backend

import (
	"os"

	"golang.org/x/sys/unix"
)

// isInteractiveTerminal reports whether f is a real terminal — one a person
// could actually answer a question on.
//
// IsTerminal is deliberately not enough for that: it asks only whether the file
// is a CHARACTER DEVICE, and /dev/null is one, so `sandboxer enter < /dev/null`
// read as interactive and printed a prompt into the void. Tolerable for
// IsTerminal's own job (choosing the engine's -t flag), not for a blocking
// question. TIOCGWINSZ is the portable isatty: it succeeds on a terminal and
// fails with ENOTTY on /dev/null, a pipe and a regular file alike — and unlike
// TCGETS/TIOCGETA it is spelled the same on every unix.
func isInteractiveTerminal(f *os.File) bool {
	_, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	return err == nil
}
