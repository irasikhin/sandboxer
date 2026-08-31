//go:build unix

package style

import (
	"os"

	"golang.org/x/sys/unix"
)

// fileIsTerminal reports whether f is a terminal. The probe is the same
// TIOCGWINSZ ioctl cli.terminalWidth uses: it succeeds only on a tty, fails
// on pipes, files and redirections — exactly the boundary color must stop at.
func fileIsTerminal(f *os.File) bool {
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	return err == nil && ws != nil
}
