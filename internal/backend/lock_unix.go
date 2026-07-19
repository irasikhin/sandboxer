//go:build unix

package backend

import (
	"os"

	"golang.org/x/sys/unix"
)

// lockFile takes an exclusive advisory lock (flock) on path, creating the file
// if needed, and returns a release func. It BLOCKS until the lock is available,
// so concurrent holders serialize. Used to serialize a sandbox's session
// create/converge across processes (see EnsureSession): two first-enters racing
// to create the same session would otherwise each bring up — and the loser tear
// down — the egress sidecar. Best-effort: on any error it returns a no-op
// release together with the error, and the caller proceeds unlocked (the
// pre-lock behavior) rather than failing the run.
func lockFile(path string) (release func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		_ = f.Close()
		return func() {}, err
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}
