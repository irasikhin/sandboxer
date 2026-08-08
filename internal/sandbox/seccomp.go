package sandbox

import (
	"os"
	"path/filepath"

	"github.com/irasikhin/sandboxer/internal/seccomp"
)

// EnsureSeccompProfile writes the nested-containers seccomp profile under
// _meta (idempotent — the name is content-addressed, so one file per profile
// version, shared by every sandbox of the project) and returns its path.
func (b *Base) EnsureSeccompProfile() (string, error) {
	if err := os.MkdirAll(b.metaDir(), 0o755); err != nil {
		return "", err
	}
	return seccomp.Write(b.metaDir())
}

// SeccompProfilePath is the path EnsureSeccompProfile would write, computed
// without touching the disk — for the read-only argv builders (show, compose)
// that must produce the exact argv enter builds without creating files.
func (b *Base) SeccompProfilePath() (string, error) {
	name, err := seccomp.FileName()
	if err != nil {
		return "", err
	}
	return filepath.Join(b.metaDir(), name), nil
}
