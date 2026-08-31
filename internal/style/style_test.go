package style

import (
	"bytes"
	"testing"
)

// The uncolored paths are the contract every test and every script relies on:
// a non-terminal writer (a buffer here — but also any pipe or file) gets the
// exact plain text, no escape codes, whatever the environment says.
func TestPlainOutput(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(w *bytes.Buffer)
		want string
	}{
		{"info", func(w *bytes.Buffer) { Infof(w, "src %s", "devops") }, "sandboxer: src devops\n"},
		{"warn", func(w *bytes.Buffer) { Warnf(w, "port %s taken", "3080") }, "sandboxer: port 3080 taken\n"},
		{"error", func(w *bytes.Buffer) { Errorf(w, "setup exited %d", 2) }, "sandboxer: setup exited 2\n"},
		{"wrap", func(w *bytes.Buffer) { w.WriteString(Wrap(w, "banner", Bold)) }, "banner"},
		{"banner", func(w *bytes.Buffer) { w.WriteString(Banner(w, "sandboxer: one\nsandboxer: two\n")) }, "sandboxer: one\nsandboxer: two\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// given
			var w bytes.Buffer
			// when
			tc.run(&w)
			// then
			if got := w.String(); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLabel(t *testing.T) {
	var w bytes.Buffer
	if got := Label(&w, BoldCyan); got != "sandboxer:" {
		t.Errorf("got %q, want plain label", got)
	}
}

// The environment opt-outs apply before the writer is even probed.
func TestEnvironmentOptOut(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var w bytes.Buffer
	if Enabled(&w) {
		t.Fatal("NO_COLOR must disable styling")
	}
	if got := Label(&w, BoldCyan); got != "sandboxer:" {
		t.Errorf("got %q, want plain label", got)
	}
	Infof(&w, "done")
	if got := w.String(); got != "sandboxer: done\n" {
		t.Errorf("got %q", got)
	}
}

func TestEnvironmentOptOutDumbTerm(t *testing.T) {
	t.Setenv("TERM", "dumb")
	var w bytes.Buffer
	if Enabled(&w) {
		t.Fatal("TERM=dumb must disable styling")
	}
}

// The colored cores are asserted directly — a real terminal cannot be faked in
// a unit test, so the gating (Enabled) and the escaping (labelColored,
// bannerColored) are tested separately.
func TestLabelColored(t *testing.T) {
	if got := labelColored(BoldCyan); got != "\x1b[1;36msandboxer:\x1b[0m" {
		t.Errorf("got %q", got)
	}
}

func TestBannerColored(t *testing.T) {
	// given
	in := "sandboxer: keep running; reattach: sandboxer enter feat\nsandboxer: done\n"
	// when
	got := bannerColored(in)
	// then
	want := "\x1b[1;36msandboxer:\x1b[0m keep running; reattach: sandboxer enter feat\n" +
		"\x1b[1;36msandboxer:\x1b[0m done\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
