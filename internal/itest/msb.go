//go:build integration

package itest

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
)

// Microsandbox returns the second microVM engine identity ("microsandbox") when
// that backend can actually run here, or skips the test — the msb twin of
// Smolvm, with the same canary stance: the binary resolves (SANDBOXER_MSB
// override or PATH), on Linux /dev/kvm exists, and a throwaway `list` round-trips.
func Microsandbox(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath(MsbBin()); err != nil {
		t.Skip("no msb on PATH (set SANDBOXER_MSB) — skipping microsandbox integration test")
	}
	if runtime.GOOS == "linux" {
		if _, err := os.Stat("/dev/kvm"); err != nil {
			t.Skip("no /dev/kvm — skipping microsandbox integration test")
		}
	}
	if err := exec.Command(MsbBin(), "list", "--format", "json").Run(); err != nil {
		t.Skip("msb not runnable here — skipping microsandbox integration test")
	}
	return "microsandbox"
}

// MsbBin is the actual msb binary the backend would exec (for direct cleanup
// calls in tests).
func MsbBin() string {
	if b := os.Getenv("SANDBOXER_MSB"); b != "" {
		return b
	}
	return "msb"
}

// MSBImage is the image a microsandbox integration test boots:
// SANDBOXER_ITEST_MSB_IMAGE when set, else the public "alpine" ref (msb pulls
// it, so that branch needs network — or a prior `msb pull alpine`).
func MSBImage() string {
	if v := os.Getenv("SANDBOXER_ITEST_MSB_IMAGE"); v != "" {
		return v
	}
	return "alpine"
}

// MSBTempDir returns a scratch directory OUTSIDE /tmp, with cleanup registered.
// It exists because the guest mounts a tmpfs over /tmp AFTER the host shares, so
// a share under /tmp is shadowed and the sandbox root "does not exist in guest"
// — the same trap msbPreflight rejects for real profiles.
func MSBTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/var/tmp", "sandboxer-msb-it-")
	if err != nil {
		t.Skipf("no writable /var/tmp for a non-/tmp share: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// CleanupSandbox registers a best-effort `msb remove -f` so a test's sandbox is
// reaped even on panic or a failed assertion.
func CleanupSandbox(t *testing.T, name string) {
	t.Helper()
	t.Cleanup(func() { _ = exec.Command(MsbBin(), "remove", "-f", name).Run() })
}
