//go:build !unix

package backend

// lockFile is a no-op on platforms without flock. sandboxer's real targets are
// unix (Linux/macOS run the container engine); a non-unix build gets no
// cross-process session serialization, matching the behavior before the lock
// existed.
func lockFile(_ string) (release func(), err error) {
	return func() {}, nil
}
