//go:build unix

package cli

import (
	"os"

	"golang.org/x/sys/unix"
)

// terminalWidth reports how many columns the terminal behind f has, or 0 when
// there is no terminal — a pipe, a file, a test buffer. Zero means "no width to
// fit", and every caller treats that as "do not truncate": output that is being
// read by a program (or captured) must stay complete.
func terminalWidth(f *os.File) int {
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws == nil {
		return 0
	}
	return int(ws.Col)
}
