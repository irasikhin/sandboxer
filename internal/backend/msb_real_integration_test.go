//go:build integration

package backend

import (
	"bytes"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/itest"
)

// hostResolves reports whether the HOST can resolve name — the "is there an
// outbound path at all" gate for the tests that need the real internet. In-
// process on purpose: shelling out to getent skipped these tests on any runner
// whose PATH lacks it (the CI pod), reading a missing binary as a missing
// network.
func hostResolves(name string) bool {
	_, err := net.LookupHost(name)
	return err == nil
}

// These exercise the REAL microsandbox backend end to end (a live msb sandbox),
// driving the exported dispatch (EnsureSession/ExecSession/…) exactly as the CLI
// does — so they verify the sandboxer wiring, not merely that msb works —
// including the name-bound egress allowlist enforced by the runner itself.
// They skip cleanly without msb/KVM (itest.Microsandbox) and boot a small POSIX
// image (itest.MSBImage), never the proprietary agents.

func msbITOpts(t *testing.T, engine, slug, dest string) RunOpts {
	t.Helper()
	t.Setenv("SANDBOXER_STATE", t.TempDir()) // isolate the machine record store (host-side only)
	return RunOpts{
		Engine: engine, Image: itest.MSBImage(), Dest: dest, MountDest: true,
		Slug: slug, BaseDir: t.TempDir(),
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
	}
}

// TestMSB_Lifecycle_RealEngine: create → exec (mounted file + exit codes) →
// reuse → stop → states → remove, all through the exported backend dispatch.
func TestMSB_Lifecycle_RealEngine(t *testing.T) {
	engine := itest.Microsandbox(t)
	dest := itest.MSBTempDir(t)
	if err := os.WriteFile(filepath.Join(dest, "hello.txt"), []byte("HI"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := msbITOpts(t, engine, "itmsblife", dest)
	name := SessionName(o.Slug, o.BaseDir)
	itest.CleanupSandbox(t, name)

	if got, err := EnsureSession(o); err != nil || got != name {
		t.Fatalf("EnsureSession = %q, %v; want %q", got, err, name)
	}

	var out bytes.Buffer
	oe := o
	oe.Stdout = &out
	if code, _ := ExecSession(oe, name, []string{"cat", filepath.Join(dest, "hello.txt")}); code != 0 || strings.TrimSpace(out.String()) != "HI" {
		t.Errorf("cat mounted file = code %d, out %q; want 0 / HI", code, out.String())
	}
	if code, _ := ExecSession(o, name, []string{"sh", "-c", "exit 7"}); code != 7 {
		t.Errorf("exit 7 = %d", code)
	}

	// Re-ensure reuses the running, fresh session (no recreate) — the identity
	// round-trips through the host-side record.
	if got, err := EnsureSession(o); err != nil || got != name {
		t.Fatalf("re-ensure = %q, %v", got, err)
	}

	if err := StopSession(engine, o.Slug, o.BaseDir); err != nil {
		t.Fatalf("StopSession: %v", err)
	}
	if states, _ := SessionStates(engine, o.BaseDir); states[o.Slug] != "stopped" {
		t.Errorf("states = %v; want %s=stopped", states, o.Slug)
	}
	// A stopped, still-fresh session is STARTED, not recreated.
	if _, err := EnsureSession(o); err != nil {
		t.Fatalf("EnsureSession after stop: %v", err)
	}
	if !InspectSession(engine, name).Running {
		t.Error("a stopped fresh session was not started")
	}

	if err := RemoveSession(engine, o.Slug, o.BaseDir); err != nil {
		t.Fatalf("RemoveSession: %v", err)
	}
	if InspectSession(engine, name).Exists {
		t.Error("sandbox still exists after remove")
	}
}

// TestMSB_NarrowingWall_RealEngine: a narrowed sandbox shares only its include
// dir — a sibling directory on the same host filesystem is unreachable inside.
func TestMSB_NarrowingWall_RealEngine(t *testing.T) {
	engine := itest.Microsandbox(t)
	root := itest.MSBTempDir(t)
	inc := filepath.Join(root, "included")
	sib := filepath.Join(root, "sibling")
	mustWrite(t, filepath.Join(inc, "ok.txt"), "OK")
	mustWrite(t, filepath.Join(sib, "secret.txt"), "LEAK")

	o := msbITOpts(t, engine, "itmsbwall", inc)
	o.MountDest = false
	o.SrcMounts = []string{inc} // only the include dir is shared
	name := SessionName(o.Slug, o.BaseDir)
	itest.CleanupSandbox(t, name)
	if _, err := EnsureSession(o); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	if code, _ := ExecSession(o, name, []string{"cat", filepath.Join(inc, "ok.txt")}); code != 0 {
		t.Errorf("included file unreadable inside the guest (code %d)", code)
	}
	if code, _ := ExecSession(o, name, []string{"cat", filepath.Join(sib, "secret.txt")}); code == 0 {
		t.Error("SECURITY: a non-included sibling directory was readable inside the guest")
	}
}

// TestMSB_GuestWriteUID_RealEngine: a file written from inside the guest lands
// on the host owned by the invoking user (no root-owned worktree).
func TestMSB_GuestWriteUID_RealEngine(t *testing.T) {
	engine := itest.Microsandbox(t)
	dest := itest.MSBTempDir(t)
	o := msbITOpts(t, engine, "itmsbuid", dest)
	name := SessionName(o.Slug, o.BaseDir)
	itest.CleanupSandbox(t, name)
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
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		t.Errorf("guest-written file uid = %d, want the host user %d", st.Uid, os.Getuid())
	}
}

// TestMSB_EgressAllowlist_RealEngine: the allowlist is enforced BY THE RUNNER,
// per name — an allowed domain resolves and connects, everything else is
// refused, and a session created with no allowlist at all is fully offline.
// Skipped when the host itself has no outbound path, so it never fails for the
// network's reasons.
func TestMSB_EgressAllowlist_RealEngine(t *testing.T) {
	engine := itest.Microsandbox(t)
	if !hostResolves("example.com") {
		t.Skip("no outbound DNS on this host — skipping the egress allowlist check")
	}
	dest := itest.MSBTempDir(t)
	o := msbITOpts(t, engine, "itmsbnet", dest)
	o.RT = config.Runtime{Egress: true, Domains: []string{"example.com"}}
	name := SessionName(o.Slug, o.BaseDir)
	itest.CleanupSandbox(t, name)
	if _, err := EnsureSession(o); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	// The allowed domain resolves inside the guest… (getent, not nslookup: it
	// exists in BOTH images this test may boot — busybox getent on the default
	// alpine, glibc getent in the toolbox image; nslookup exists in neither
	// guaranteed form and cost this test a 127 on the real image.)
	if code, _ := ExecSession(o, name, []string{"sh", "-c", "getent hosts example.com >/dev/null 2>&1"}); code != 0 {
		t.Errorf("the allowed domain did not resolve inside the guest (code %d)", code)
	}
	// …and a domain that is not on the list is refused.
	if code, _ := ExecSession(o, name, []string{"sh", "-c",
		"wget -q -T 5 -O /dev/null http://api.github.com/ 2>/dev/null"}); code == 0 {
		t.Error("SECURITY: a domain outside the allowlist was reachable from the guest")
	}

	// An empty allowlist is a valid, fully offline state (not an error).
	off := msbITOpts(t, engine, "itmsboff", dest)
	off.RT = config.Runtime{Egress: true}
	offName := SessionName(off.Slug, off.BaseDir)
	itest.CleanupSandbox(t, offName)
	if _, err := EnsureSession(off); err != nil {
		t.Fatalf("EnsureSession (offline): %v", err)
	}
	if code, _ := ExecSession(off, offName, []string{"sh", "-c", "nslookup example.com >/dev/null 2>&1"}); code == 0 {
		t.Error("SECURITY: an empty allowlist still had outbound DNS")
	}
}

// TestMSB_NestedContainer_RealEngine verifies the load-bearing claim that a
// microsandbox guest "runs container engines natively" (the reason the
// nestedContainers knob is ignored and the postgres-in-the-sandbox use case that
// started the migration): docker/podman inside the toolbox image boot, pull and
// run, and a USER-SWITCHING image works against the guest's own kernel. It
// skips unless a REAL toolbox image (which carries docker/podman/compose) is
// pointed at via SANDBOXER_ITEST_MSB_IMAGE — the default alpine itest image has
// no engine to probe.
func TestMSB_NestedContainer_RealEngine(t *testing.T) {
	engine := itest.Microsandbox(t)
	if os.Getenv("SANDBOXER_ITEST_MSB_IMAGE") == "" {
		t.Skip("nested-container check needs the REAL toolbox image — set SANDBOXER_ITEST_MSB_IMAGE to the toolbox tar (it carries docker/podman/compose)")
	}
	if !hostResolves("registry-1.docker.io") {
		t.Skip("no outbound DNS on this host — skipping the nested-container check")
	}
	dest := itest.MSBTempDir(t)
	// The guest pulls from Docker Hub, so the allowlist covers the registry —
	// including BOTH blob CDNs: Hub has served blobs from cloudflare.docker.com
	// historically and from cloudfront.docker.com since 2026 (observed live —
	// the name-bound deny on the new CDN host failed every pull with 125).
	o := msbITOpts(t, engine, "itmsbctr", dest)
	o.RT = config.Runtime{Egress: true, Domains: []string{
		"docker.io", "registry-1.docker.io", "auth.docker.io",
		"production.cloudflare.docker.com",
		"production.cloudfront.docker.com",
	}}
	name := SessionName(o.Slug, o.BaseDir)
	itest.CleanupSandbox(t, name)
	if _, err := EnsureSession(o); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	// The toolbox image carries the engines; the guest runs them natively.
	if code, _ := ExecSession(o, name, []string{"docker", "--version"}); code != 0 {
		t.Fatalf("docker absent inside the guest (code %d)", code)
	}
	if code, _ := ExecSession(o, name, []string{"podman", "--version"}); code != 0 {
		t.Fatalf("podman absent inside the guest (code %d)", code)
	}
	// A plain container pulls and runs.
	if code, _ := ExecSession(o, name, []string{"docker", "run", "--rm",
		"docker.io/library/alpine:3.21", "sh", "-c", "echo NESTED-OK"}); code != 0 {
		t.Errorf("docker run alpine inside the guest = %d, want 0", code)
	}
	// An explicit non-root container user maps correctly.
	if code, _ := ExecSession(o, name, []string{"docker", "run", "--rm", "--user", "999:999",
		"docker.io/library/alpine:3.21", "id", "-u"}); code != 0 {
		t.Errorf("docker run --user 999:999 inside the guest = %d, want 0", code)
	}

	// The REAL user-switching case: postgres started as a SERVICE. `docker run
	// --rm postgres id` would be a false pass — `id` replaces the image's CMD,
	// so the entrypoint skips the data-dir chown and the gosu step-down (it
	// gates both on `[ "$1" = 'postgres' ]`), and the command exits 0 even where
	// the uid machinery is broken. Start it, wait for readiness, run a query and
	// check the uid it actually drops to.
	if code, _ := ExecSession(o, name, []string{"docker", "run", "-d", "--name", "pg",
		"-e", "POSTGRES_PASSWORD=x", "docker.io/library/postgres:16-alpine"}); code != 0 {
		t.Fatalf("docker run -d postgres inside the guest = %d, want 0", code)
	}
	ready := []string{"sh", "-c",
		"for i in $(seq 1 60); do docker exec pg pg_isready -U postgres >/dev/null 2>&1 && exit 0; sleep 1; done; exit 1"}
	if code, _ := ExecSession(o, name, ready); code != 0 {
		t.Errorf("postgres never became ready inside the guest (code %d)", code)
	}
	var qOut bytes.Buffer
	qo := o
	qo.Stdout = &qOut
	if code, _ := ExecSession(qo, name, []string{"docker", "exec", "pg",
		"psql", "-U", "postgres", "-tAc", "select 42"}); code != 0 || strings.TrimSpace(qOut.String()) != "42" {
		t.Errorf("postgres query = %q (code %d), want 42", qOut.String(), code)
	}
	var idOut bytes.Buffer
	io2 := o
	io2.Stdout = &idOut
	if code, _ := ExecSession(io2, name, []string{"docker", "exec", "pg",
		"id", "-u", "postgres"}); code != 0 || strings.TrimSpace(idOut.String()) != "70" {
		t.Errorf("postgres uid = %q (code %d), want the postgres user (70)", idOut.String(), code)
	}
}

// TestMSB_SecretsMode_RealEngine pins the opt-in host-scoped secret channel on a
// live sandbox: the real VALUE never enters the host process table (only a KEY
// reference does) and never lands in the machine's own configuration.
func TestMSB_SecretsMode_RealEngine(t *testing.T) {
	engine := itest.Microsandbox(t)
	t.Setenv(msbSecretsEnv, "1")
	const token = "sup3rsecret-msb-itest-value"
	t.Setenv("IT_SECRET_TOKEN", token)

	dest := itest.MSBTempDir(t)
	o := msbITOpts(t, engine, "itmsbsec", dest)
	o.AuthEnv = []string{"IT_SECRET_TOKEN=" + token}
	o.RT = config.Runtime{Egress: true, Domains: []string{"example.com"}}
	name := SessionName(o.Slug, o.BaseDir)
	itest.CleanupSandbox(t, name)
	if _, err := EnsureSession(o); err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}

	out, err := exec.Command(itest.MsbBin(), "inspect", name, "--format", "json").Output()
	if err != nil {
		t.Fatalf("msb inspect: %v", err)
	}
	if strings.Contains(string(out), token) {
		t.Error("SECURITY: the secret VALUE was persisted in the sandbox configuration")
	}
	if scanProcCmdlines(t, token) {
		t.Error("SECURITY: the secret VALUE appeared in a host process command line")
	}
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
