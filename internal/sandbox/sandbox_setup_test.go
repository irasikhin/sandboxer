package sandbox

import "testing"

// TestSetupPendingAndMark exercises the run-once setup gate: an empty script
// never pends, a fresh script pends with a stable hash, marking it done clears
// the pend, and editing the script re-pends with a different hash.
func TestSetupPendingAndMark(t *testing.T) {
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if p, _ := b.SetupPending("s", "   \n  "); p {
		t.Error("blank script must not pend")
	}

	pending, hash := b.SetupPending("s", "npm ci")
	if !pending || hash == "" {
		t.Fatalf("fresh script must pend with a hash; got pending=%v hash=%q", pending, hash)
	}

	if err := b.MarkSetupDone("s", hash); err != nil {
		t.Fatal(err)
	}
	if p, _ := b.SetupPending("s", "npm ci"); p {
		t.Error("same script must not pend after MarkSetupDone")
	}

	p, h := b.SetupPending("s", "npm ci && npm test")
	if !p {
		t.Error("edited script must pend again")
	}
	if h == hash {
		t.Error("edited script must produce a different hash")
	}

	// The stamp is per-slug: another sandbox with the same script still pends.
	if p, _ := b.SetupPending("other", "npm ci"); !p {
		t.Error("a different slug must pend independently")
	}
}

// TestRemoveClearsSetupStamp ensures removing a sandbox also drops its setup
// stamp, so a recreated sandbox of the same name re-runs setup.
func TestRemoveClearsSetupStamp(t *testing.T) {
	b, err := ResolveBase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, hash := b.SetupPending("s", "make")
	if err := b.MarkSetupDone("s", hash); err != nil {
		t.Fatal(err)
	}
	if err := b.Remove("s"); err != nil {
		t.Fatal(err)
	}
	if p, _ := b.SetupPending("s", "make"); !p {
		t.Error("setup must pend again after the sandbox is removed")
	}
}
