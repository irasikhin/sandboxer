//go:build integration

// Package itest holds shared helpers for the build-tagged real end-to-end
// integration suite (run with `-tags integration`). Every file here carries the
// integration build constraint, so the package compiles to nothing in a normal
// build: it never enters the production binary and never affects the coverage
// gate. It exists because Go test helpers in `*_test.go` are package-scoped and
// cannot be shared across packages; a regular (tagged) package can.
//
// The load-bearing convention across the whole suite: the container engine, the
// networks and the egress proxy are REAL; the coding agent is always a stub. The
// proprietary claude/codex/opencode binaries are never invoked.
package itest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
)

var counter atomic.Int64

// Slug returns a per-test unique, sanitize-safe name "<prefix>-<pid>-<n>". It
// deliberately avoids time/rand (kept deterministic) and mirrors egress.Up's own
// sbx-<slug>-<pid> scheme, so any leaked container/network is easy to spot.
func Slug(prefix string) string {
	return prefix + "-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(counter.Add(1), 10)
}

// WriteStub writes an executable #!/bin/sh script at dir/name and returns its
// path. Used for stub agents and in-container probes.
func WriteStub(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// BuildBinary compiles a static sandboxer binary (CGO disabled, so it runs on a
// musl smoke image such as alpine) and returns its path. It skips the test when
// `go` is not available to build with.
func BuildBinary(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH — cannot build the sandboxer binary for the in-container proxy")
	}
	out := filepath.Join(t.TempDir(), "sandboxer")
	cmd := exec.Command("go", "build", "-o", out, "github.com/irasikhin/sandboxer/cmd/sandboxer")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build sandboxer: %v\n%s", err, b)
	}
	return out
}
