//go:build !unix

package cli

import "os"

// terminalWidth fallback for non-unix platforms, which have no tty ioctl here:
// no width is known, so nothing is truncated (see terminalWidth in
// termwidth_unix.go). sandboxer's real targets are unix.
func terminalWidth(_ *os.File) int { return 0 }
