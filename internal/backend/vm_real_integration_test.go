//go:build integration

package backend

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/irasikhin/sandboxer/internal/itest"
)

// These exercise the REAL microVM backend end to end (a live smolvm machine),
// driving the exported dispatch (EnsureSession/ExecSession/…) exactly as the CLI
// does — so they verify the sandboxer wiring, not merely that smolvm works. They
// skip cleanly without smolvm/KVM (itest.Smolvm) and boot a small POSIX image
// (itest.VMImage), never the proprietary agents.

func vmITOpts(t *testing.T, engine, slug, dest string) RunOpts {
	t.Helper()
	t.Setenv("SANDBOXER_STATE", t.TempDir()) // isolate the machine record store
	return RunOpts{
		Engine: engine, Image: itest.VMImage(), Dest: dest, MountDest: true,
		Slug: slug, BaseDir: t.TempDir(),
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
	}
}

// TestVM_Lifecycle_RealEngine: create → exec (mounted file + exit codes) → stop
// → states → remove, all through the exported backend dispatch.
func TestVM_Lifecycle_RealEngine(t *testing.T) {
	engine := itest.Smolvm(t)
	dest := t.TempDir()
	if err := os.WriteFile(filepath.Join(dest, "hello.txt"), []byte("HI"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := vmITOpts(t, engine, "itlife", dest)
	name := SessionName(o.Slug, o.BaseDir)
	itest.CleanupMachine(t, name)

	if got, err := EnsureSession(o); err != nil || got != name {
		t.Fatalf("EnsureSession = %q, %v; want %q", got, err, name)
	}

	// The mounted sandbox root is readable inside the guest.
	var out bytes.Buffer
	oe := o
	oe.Stdout = &out
	if code, _ := ExecSession(oe, name, []string{"cat", filepath.Join(dest, "hello.txt")}); code != 0 || out.String() != "HI" {
		t.Errorf("cat mounted file = code %d, out %q; want 0 / HI", code, out.String())
	}
	// Exit codes propagate.
	if code, _ := ExecSession(o, name, []string{"sh", "-c", "exit 7"}); code != 7 {
		t.Errorf("exit 7 = %d", code)
	}

	// Re-ensure reuses the running, fresh session (no recreate).
	if got, err := EnsureSession(o); err != nil || got != name {
		t.Fatalf("re-ensure = %q, %v", got, err)
	}

	if err := StopSession(engine, o.Slug, o.BaseDir); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	if states, _ := SessionStates(engine, o.BaseDir); states[o.Slug] != "stopped" {
		t.Errorf("states = %v; want %s=stopped", states, o.Slug)
	}
	if err := RemoveSession(engine, o.Slug, o.BaseDir); err != nil {
		t.Fatalf("RemoveSession: %v", err)
	}
	if InspectSession(engine, name).Exists {
		t.Error("machine still exists after remove")
	}
}

// TestVM_NarrowingWall_RealEngine: a narrowed sandbox shares only its include
// dir — a sibling directory on the same host filesystem is unreachable inside.
func TestVM_NarrowingWall_RealEngine(t *testing.T) {
	engine := itest.Smolvm(t)
	root := t.TempDir()
	inc := filepath.Join(root, "included")
	sib := filepath.Join(root, "sibling")
	mustWrite(t, filepath.Join(inc, "ok.txt"), "OK")
	mustWrite(t, filepath.Join(sib, "secret.txt"), "LEAK")

	o := vmITOpts(t, engine, "itwall", inc)
	o.MountDest = false
	o.SrcMounts = []string{inc} // only the include dir is shared
	name := SessionName(o.Slug, o.BaseDir)
	itest.CleanupMachine(t, name)
	if _, err := EnsureSession(o); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	if code, _ := ExecSession(o, name, []string{"cat", filepath.Join(inc, "ok.txt")}); code != 0 {
		t.Errorf("included file unreadable inside the guest (code %d)", code)
	}
	// The sibling is NOT mounted → unreachable. A zero exit here would mean the
	// wall leaked.
	if code, _ := ExecSession(o, name, []string{"cat", filepath.Join(sib, "secret.txt")}); code == 0 {
		t.Error("SECURITY: a non-included sibling directory was readable inside the guest")
	}
}

// TestVM_GuestWriteUID_RealEngine: a file written from inside the guest lands on
// the host owned by the invoking user (no root-owned worktree).
func TestVM_GuestWriteUID_RealEngine(t *testing.T) {
	engine := itest.Smolvm(t)
	dest := t.TempDir()
	o := vmITOpts(t, engine, "ituid", dest)
	name := SessionName(o.Slug, o.BaseDir)
	itest.CleanupMachine(t, name)
	if _, err := EnsureSession(o); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	target := filepath.Join(dest, "from-guest.txt")
	if code, _ := ExecSession(o, name, []string{"sh", "-c", "echo GUEST > " + target}); code != 0 {
		t.Fatalf("guest write failed (code %d)", code)
	}
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatalf("guest-written file not on host: %v", err)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		if int(st.Uid) != os.Getuid() {
			t.Errorf("guest-written file uid = %d, want the host user %d", st.Uid, os.Getuid())
		}
	}
}

// TestVM_SecretNotInPS_RealEngine: an auth secret injected for an exec never
// appears in the host process table (it rides --secret-env, not argv).
func TestVM_SecretNotInPS_RealEngine(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("ps/proc scan is linux-only")
	}
	engine := itest.Smolvm(t)
	dest := t.TempDir()
	o := vmITOpts(t, engine, "itsec", dest)
	o.AuthEnv = []string{"IT_SECRET_TOKEN=sup3rsecret-itest-value"}
	name := SessionName(o.Slug, o.BaseDir)
	itest.CleanupMachine(t, name)
	if _, err := EnsureSession(o); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = ExecSession(o, name, []string{"sh", "-c", "sleep 2"})
	}()
	time.Sleep(700 * time.Millisecond)
	if scanProcCmdlines(t, "sup3rsecret-itest-value") {
		t.Error("SECURITY: the auth secret appeared in a host process command line")
	}
	<-done
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// scanProcCmdlines reports whether needle appears in any /proc/*/cmdline.
func scanProcCmdlines(t *testing.T, needle string) bool {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Skip("no /proc")
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		if strings.Contains(string(bytes.ReplaceAll(data, []byte{0}, []byte{' '})), needle) {
			return true
		}
	}
	return false
}
