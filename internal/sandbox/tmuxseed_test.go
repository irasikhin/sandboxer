package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The image's /etc/tmux.conf is the real home for these settings, but images are
// rebuilt rarely; seeding the sandbox home makes them effective on an image
// built before that, and on sandboxes that already exist. These pin the two
// properties that matter: it lands, and it never clobbers the user's own file.

func TestEnsureHomeSeedsTmuxConf(t *testing.T) {
	b := &Base{Src: t.TempDir(), Dir: t.TempDir()}
	if err := b.EnsureHome("box"); err != nil {
		t.Fatalf("EnsureHome: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(b.HomeDir("box"), ".tmux.conf"))
	if err != nil {
		t.Fatalf("no seeded ~/.tmux.conf: %v", err)
	}
	for _, want := range []string{
		"extended-keys on",
		"extended-keys-format csi-u",
		`terminal-features ",*:extkeys"`,
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("seeded tmux.conf missing %q — modified keys stay flattened", want)
		}
	}
}

func TestEnsureHomeNeverOverwritesTmuxConf(t *testing.T) {
	b := &Base{Src: t.TempDir(), Dir: t.TempDir()}
	if err := b.EnsureHome("box"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(b.HomeDir("box"), ".tmux.conf")

	// The user makes it theirs — including deliberately emptying it.
	if err := os.WriteFile(path, []byte("# mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := b.EnsureHome("box"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "# mine\n" {
		t.Errorf("sandboxer rewrote the user's ~/.tmux.conf: %q", got)
	}
}

// EnsureHome runs on every enter/exec; seeding must not make it fail when the
// home is not writable — the enter has bigger problems to report than a tmux
// nicety.
func TestEnsureHomeTmuxSeedIsBestEffort(t *testing.T) {
	b := &Base{Src: t.TempDir(), Dir: t.TempDir()}
	if err := b.EnsureHome("box"); err != nil {
		t.Fatal(err)
	}
	home := b.HomeDir("box")
	_ = os.Remove(filepath.Join(home, ".tmux.conf"))
	if err := os.Chmod(home, 0o500); err != nil { // read-only home
		t.Skip("cannot chmod in this environment")
	}
	t.Cleanup(func() { _ = os.Chmod(home, 0o700) })
	if err := b.EnsureHome("box"); err != nil {
		t.Errorf("EnsureHome should not fail because the tmux seed could not be written: %v", err)
	}
}
