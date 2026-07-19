//go:build unix

package backend

import (
	"path/filepath"
	"testing"
	"time"
)

// TestLockFileSerializes: a second lockFile on the same path blocks until the
// first releases — the cross-process serialization EnsureSession relies on so a
// concurrent first-enter cannot tear down the winner's egress sidecar (M7).
func TestLockFileSerializes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.lock")
	rel1, err := lockFile(path)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}

	got := make(chan struct{})
	go func() {
		rel2, err := lockFile(path) // must block until rel1() runs
		if err == nil {
			rel2()
		}
		close(got)
	}()

	// While the first lock is held, the second must not acquire.
	select {
	case <-got:
		t.Fatal("second lockFile acquired while the first was held")
	case <-time.After(150 * time.Millisecond):
	}

	rel1()
	// Once released, the second must acquire promptly.
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("second lockFile did not acquire after release")
	}
}
