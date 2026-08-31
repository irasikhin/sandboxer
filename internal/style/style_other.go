//go:build !unix

package style

import "os"

// fileIsTerminal — no styling on non-unix hosts (Windows/WSL2 compile but are
// not live-verified; uncolored output is always correct).
func fileIsTerminal(*os.File) bool { return false }
