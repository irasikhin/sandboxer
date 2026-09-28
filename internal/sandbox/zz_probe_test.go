//go:build unix

package sandbox

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestZZZFileidFSProbe is a TEMPORARY diagnostic (probe branch only): how
// often is a same-path rmdir+mkdir indistinguishable on this test host's
// filesystem, with and without a delay? It exists to decide whether the
// TestStatIdentityChangesOnRecreate flake is timestamp granularity (fixed by
// a delay) or inode-cache reuse (not).
func TestZZZFileidFSProbe(t *testing.T) {
	t.Logf("probe fs=%s", fsType(t.TempDir()))
	measure := func(tag string, pause time.Duration, n int) {
		same := 0
		var sample string
		for i := 0; i < n; i++ {
			d := filepath.Join(t.TempDir(), "view")
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
			before, okB := statIdentity(d)
			if err := os.RemoveAll(d); err != nil {
				t.Fatal(err)
			}
			if pause > 0 {
				time.Sleep(pause)
			}
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
			after, okA := statIdentity(d)
			if !okB || !okA {
				t.Fatalf("statIdentity ok flags: before=%v after=%v", okB, okA)
			}
			if after == before {
				same++
				if sample == "" {
					sample = before
				}
			}
		}
		t.Logf("probe %s: same=%d/%d sample=%s", tag, same, n, sample)
	}
	measure("immediate", 0, 3000)
	measure("pause20ms", 20*time.Millisecond, 300)
	measure("pause1s", time.Second, 20)
}
