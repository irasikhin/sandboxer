//go:build unix

package sandbox

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// statIdentity returns a string uniquely identifying the file OBJECT at path,
// for MountFingerprint, or ("", false) when it cannot be stat'd. A bind mount is
// pinned to that object, so the identity must change when the host REPLACES the
// directory (rmdir+mkdir, as a git checkout can) and stay put when only its
// CONTENTS change (the agent adding files through the mount) — otherwise a live
// session would either miss an orphaned mount or rebuild on every edit.
//
// The identity is device + inode NUMBER + inode BIRTH TIME, because neither
// replacement signal is sufficient alone:
//   - the inode number changes on replacement on most filesystems, but ext4
//     recycles a just-freed number, so a recreate can reuse it;
//   - the birth time is fresh for the new inode even when its number is reused,
//     but btrfs can stamp two rapid creations with the same btime.
//
// Together they cover both: btrfs distinguishes by inode number, ext4 by birth
// time. Both are immutable once the inode exists, so neither moves when the
// directory's contents change. When statx or STATX_BTIME is unavailable (an old
// kernel, or a filesystem without birth times) it falls back to device+inode:
// weaker against inode reuse, but it never spuriously rebuilds, which is the
// safer failure.
func statIdentity(path string) (string, bool) {
	var stx unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, 0, unix.STATX_INO|unix.STATX_BTIME, &stx); err == nil {
		id := fmt.Sprintf("%d:%d:%d", stx.Dev_major, stx.Dev_minor, stx.Ino)
		if stx.Mask&unix.STATX_BTIME != 0 {
			id += fmt.Sprintf(":%d.%09d", stx.Btime.Sec, stx.Btime.Nsec)
		}
		return id, true
	}
	// statx unsupported (old kernel) — fall back to device+inode via fstat.
	fi, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%d:%d", st.Dev, st.Ino), true
	}
	return fmt.Sprintf("sz%d:mt%d", fi.Size(), fi.ModTime().UnixNano()), true
}
