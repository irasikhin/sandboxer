//go:build integration

// Package itest holds shared helpers for the build-tagged real end-to-end
// integration suite (run with `-tags integration`). Every file here carries the
// integration build constraint, so the package compiles to nothing in a normal
// build: it never enters the production binary and never affects the coverage
// gate. It exists because Go test helpers in `*_test.go` are package-scoped and
// cannot be shared across packages; a regular (tagged) package can.
//
// The load-bearing convention across the whole suite: the microVM runtime (msb)
// and its network policy are REAL; the coding agent is always a stub. The
// proprietary claude/codex/opencode binaries are never invoked.
package itest

import (
	"os"
	"strconv"
	"sync/atomic"
)

var counter atomic.Int64

// Slug returns a per-test unique, sanitize-safe name "<prefix>-<pid>-<n>". It
// deliberately avoids time/rand (kept deterministic), so any leaked machine is
// easy to attribute to its test run.
func Slug(prefix string) string {
	return prefix + "-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(counter.Add(1), 10)
}
