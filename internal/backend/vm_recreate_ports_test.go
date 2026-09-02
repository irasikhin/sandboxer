package backend

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/irasikhin/sandboxer/internal/config"
)

// listenLocal opens a real listener on 127.0.0.1:<port> and hands back its
// address — the test's stand-in for whatever holds a host port.
func listenLocal(t *testing.T, port int) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("listen :%d: %v", port, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

func portRT(host, guest int) RunOpts {
	return RunOpts{
		Engine:  msbEngine,
		Image:   "img:1",
		Dest:    "/d",
		HomeDir: "/h",
		RT:      config.Runtime{Ports: []config.Port{{Bind: "127.0.0.1", Host: host, Guest: guest, Proto: "tcp"}}},
	}
}

func TestVMPortsPreflightExcept(t *testing.T) {
	held := listenLocal(t, 0)
	addr := held.Addr().String()
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	o := portRT(port, port)

	// strict: the held port must be reported
	err = vmPortsPreflight(o)
	if err == nil || !strings.Contains(err.Error(), addr) {
		t.Errorf("vmPortsPreflight on a held port: err = %v, want an in-use error naming %s", err, addr)
	}

	// exempt: the same addr skips the check (the recreate case)
	if err := vmPortsPreflightExcept(o, map[string]bool{addr: true}); err != nil {
		t.Errorf("vmPortsPreflightExcept with the held addr exempt: %v, want nil", err)
	}

	// a free port passes both forms
	free := portRT(port+1, port+1)
	if err := vmPortsPreflight(free); err != nil {
		t.Errorf("vmPortsPreflight on a free port: %v", err)
	}
	if err := vmPortsPreflightExcept(free, nil); err != nil {
		t.Errorf("vmPortsPreflightExcept on a free port: %v", err)
	}

	// udp forwards are not probed (only tcp can be bound here)
	udp := RunOpts{Engine: msbEngine, Dest: "/d", HomeDir: "/h",
		RT: config.Runtime{Ports: []config.Port{{Bind: "127.0.0.1", Host: port, Guest: port, Proto: "udp"}}}}
	if err := vmPortsPreflight(udp); err != nil {
		t.Errorf("vmPortsPreflight on a udp forward: %v, want nil (tcp-only probe)", err)
	}
}

func TestVMWaitPortsFree(t *testing.T) {
	o := portRT(39081, 39081)

	// free: returns immediately
	if err := vmWaitPortsFree(o); err != nil {
		t.Errorf("vmWaitPortsFree with a free port: %v", err)
	}

	// released shortly after: the dying-VM case — must wait it out
	ln := listenLocal(t, 39081)
	go func() {
		time.Sleep(400 * time.Millisecond)
		_ = ln.Close()
	}()
	if err := vmWaitPortsFree(o); err != nil {
		t.Errorf("vmWaitPortsFree with a port released after 400ms: %v", err)
	}

	// held past the deadline: the actionable in-use error, not a hang
	ln = listenLocal(t, 39081)
	defer func() { _ = ln.Close() }()
	old := vmPortReleaseTimeout
	vmPortReleaseTimeout = 50 * time.Millisecond
	t.Cleanup(func() { vmPortReleaseTimeout = old })
	err := vmWaitPortsFree(o)
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1:39081") {
		t.Errorf("vmWaitPortsFree with a port held past the deadline: err = %v, want the in-use error", err)
	}
}

// TestMSBRecreateForeignPortHolder pins the failure the user hit: a recreate
// must fail BEFORE removing the old machine when the forward is held by
// someone else — the session stays intact, nothing is torn down.
func TestMSBRecreateForeignPortHolder(t *testing.T) {
	// the preflight refuses anything under /tmp (the guest tmpfs) — keep the
	// test dirs off it
	t.Setenv("TMPDIR", "/var/tmp")
	log := setupFakeMSB(t)
	base := t.TempDir()
	o := portRT(39082, 39082)
	o.Slug, o.BaseDir, o.Dest, o.HomeDir = "s", base, base, filepath.Join(base, "home")
	o.Stderr, o.Stdout = &bytes.Buffer{}, &bytes.Buffer{}
	name := SessionName("s", base)

	// the old machine runs, and it publishes NOTHING on the contested port
	machines := os.Getenv("FAKE_MACHINES")
	if err := os.MkdirAll(machines, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(machines, name), []byte("Running"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig := msbSessionPorts
	msbSessionPorts = func(string) []config.Port { return nil }
	t.Cleanup(func() { msbSessionPorts = orig })

	_ = listenLocal(t, 39082) // someone ELSE holds the port, and keeps it

	_, err := vmRecreateSession(o, name, "hash")
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1:39082") {
		t.Fatalf("vmRecreateSession with a foreign holder: err = %v, want the in-use error", err)
	}
	if _, statErr := os.Stat(filepath.Join(machines, name)); statErr != nil {
		t.Errorf("the old machine must survive a failed recreate: %v", statErr)
	}
	for _, forbidden := range []string{"remove", "create"} {
		if strings.Contains(readFile(t, log), forbidden) {
			t.Errorf("the failed recreate must not touch the machine, but the log contains %q:\n%s", forbidden, readFile(t, log))
		}
	}
}

// TestMSBRecreateOwnPortReleased pins the healthy recreate of a session whose
// forward IS published by the machine being replaced: the port is exempt from
// the pre-recreate check (the removal releases it), and the wait that follows
// the removal rides out the release lag instead of failing the create.
func TestMSBRecreateOwnPortReleased(t *testing.T) {
	t.Setenv("TMPDIR", "/var/tmp")
	log := setupFakeMSB(t)
	base := t.TempDir()
	o := portRT(39083, 39083)
	o.Slug, o.BaseDir, o.Dest, o.HomeDir = "s", base, base, filepath.Join(base, "home")
	o.Stderr, o.Stdout = &bytes.Buffer{}, &bytes.Buffer{}
	name := SessionName("s", base)

	machines := os.Getenv("FAKE_MACHINES")
	if err := os.MkdirAll(machines, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(machines, name), []byte("Running"), 0o644); err != nil {
		t.Fatal(err)
	}
	// the old machine publishes the contested port (like msb inspect would say)
	orig := msbSessionPorts
	msbSessionPorts = func(string) []config.Port {
		return []config.Port{{Bind: "127.0.0.1", Host: 39083, Guest: 39083, Proto: "tcp"}}
	}
	t.Cleanup(func() { msbSessionPorts = orig })

	// the dying VM releases the port shortly after the removal — the measured
	// ~2 s lag, compressed
	ln := listenLocal(t, 39083)
	go func() {
		time.Sleep(400 * time.Millisecond)
		_ = ln.Close()
	}()

	if _, err := vmRecreateSession(o, name, "hash"); err != nil {
		t.Fatalf("vmRecreateSession with the old machine's own forward: %v", err)
	}
	got := readFile(t, log)
	if i, j := strings.Index(got, "remove"), strings.Index(got, "create"); i < 0 || j < 0 || i > j {
		t.Errorf("recreate must remove then create, got log:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(machines, name)); err != nil {
		t.Errorf("the recreated machine must exist: %v", err)
	}
	// the host-side record is rewritten by the create
	rec := readVMRecord(name)
	if rec.Hash != "hash" {
		t.Errorf("record hash = %q, want %q", rec.Hash, "hash")
	}
}
