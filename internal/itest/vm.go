//go:build integration

package itest

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
)

// Smolvm returns the microVM engine identity ("smolvm") when the backend can
// actually run here, or skips the test. "Can run" means the binary resolves
// (SANDBOXER_SMOLVM override or PATH), on Linux /dev/kvm exists, and a throwaway
// `machine ls` round-trips — mirroring Engine's canary stance so the suite skips
// rather than fails on a host without a hypervisor. The real binary is whatever
// SANDBOXER_SMOLVM/PATH resolves; the returned string is the engine identity the
// backend dispatches on.
func Smolvm(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("SANDBOXER_SMOLVM")
	if bin == "" {
		bin = "smolvm"
	}
	if _, err := exec.LookPath(bin); err != nil {
		t.Skip("no smolvm on PATH (set SANDBOXER_SMOLVM) — skipping microvm integration test")
	}
	if runtime.GOOS == "linux" {
		if _, err := os.Stat("/dev/kvm"); err != nil {
			t.Skip("no /dev/kvm — skipping microvm integration test")
		}
	}
	if err := exec.Command(bin, "machine", "ls", "--json").Run(); err != nil {
		t.Skip("smolvm not runnable here — skipping microvm integration test")
	}
	return "smolvm"
}

// SmolvmBin is the actual smolvm binary the backend would exec (for direct
// cleanup calls in tests).
func SmolvmBin() string {
	if b := os.Getenv("SANDBOXER_SMOLVM"); b != "" {
		return b
	}
	return "smolvm"
}

// VMImage is the image a microVM integration test boots: SANDBOXER_ITEST_VM_IMAGE
// (a docker-save tar path or a public ref) when set, else the public "alpine"
// ref (pulled by smolvm, so this branch needs network). A small POSIX image is
// enough for the wall/egress/uid/lifecycle checks — the toolbox image (tmux+ps)
// is only needed for the session-restore tests.
func VMImage() string {
	if v := os.Getenv("SANDBOXER_ITEST_VM_IMAGE"); v != "" {
		return v
	}
	return "alpine"
}

// CleanupMachine registers a best-effort `machine delete -f` so a test's machine
// is reaped even on panic or a failed assertion.
func CleanupMachine(t *testing.T, name string) {
	t.Helper()
	t.Cleanup(func() { _ = exec.Command(SmolvmBin(), "machine", "delete", "--name", name, "-f").Run() })
}
