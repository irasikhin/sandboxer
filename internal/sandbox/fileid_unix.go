//go:build unix

package sandbox

import (
	"fmt"
	"io/fs"
	"syscall"
)

// fileIdentity returns a "device:inode" string uniquely identifying the file
// object behind fi. A bind mount is pinned to this identity, so a change in it
// means the host recreated the directory and any live mount of it is now
// orphaned (see MountFingerprint). Falls back to a size/mtime identity if the
// platform stat is somehow not the expected shape — enough to still flip the
// fingerprint on a real change.
func fileIdentity(fi fs.FileInfo) string {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%d:%d", st.Dev, st.Ino)
	}
	return fmt.Sprintf("sz%d:mt%d", fi.Size(), fi.ModTime().UnixNano())
}
