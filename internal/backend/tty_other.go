//go:build !unix

package backend

import "os"

// isInteractiveTerminal fallback for non-unix platforms, which have no tty
// ioctl here. It degrades to the character-device test, matching IsTerminal:
// sandboxer's real targets are unix (see tty_unix.go), and the cost of being
// wrong is a prompt nobody reads, never a hang — the prompt treats an
// unreadable answer as "no".
func isInteractiveTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
