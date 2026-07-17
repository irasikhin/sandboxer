//go:build !unix

package sandbox

import (
	"fmt"
	"io/fs"
)

// fileIdentity fallback for non-unix platforms, which lack a stable inode via
// syscall.Stat_t: a size/mtime pair is coarser but still flips the mount
// fingerprint when a directory is recreated. sandboxer's supported targets are
// unix (see fileid_unix.go); this only keeps the package compiling elsewhere.
func fileIdentity(fi fs.FileInfo) string {
	return fmt.Sprintf("sz%d:mt%d", fi.Size(), fi.ModTime().UnixNano())
}
