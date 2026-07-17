//go:build !unix

package sandbox

import (
	"fmt"
	"os"
)

// statIdentity fallback for non-unix platforms, which lack statx/inode birth
// times: a size/mtime pair is coarser but still changes when a directory is
// recreated. sandboxer's supported targets are unix (see fileid_unix.go); this
// only keeps the package compiling elsewhere. Returns ("", false) when the path
// cannot be stat'd, so a vanished mount still flips the fingerprint.
func statIdentity(path string) (string, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	return fmt.Sprintf("sz%d:mt%d", fi.Size(), fi.ModTime().UnixNano()), true
}
