package cli

import (
	"strings"
	"testing"
)

// TestInteractiveShellArgs pins the guarded launcher: bash -c with the baked rc
// path, the --rcfile invocation, and the plain-shell fallback for older images.
func TestInteractiveShellArgs(t *testing.T) {
	got := interactiveShellArgs()
	if len(got) != 3 || got[0] != "bash" || got[1] != "-c" {
		t.Fatalf("want [bash -c <script>], got %v", got)
	}
	script := got[2]
	for _, want := range []string{"/etc/sandboxer/rc.sh", "--rcfile", "exec bash -i"} {
		if !strings.Contains(script, want) {
			t.Errorf("launcher missing %q in %q", want, script)
		}
	}
}

// TestEnterBanner checks the orientation notice carries the slug, engine and dir.
func TestEnterBanner(t *testing.T) {
	b := enterBanner("feat", "podman", "/p/.sandboxer/feat")
	for _, want := range []string{`"feat"`, "podman", "/p/.sandboxer/feat"} {
		if !strings.Contains(b, want) {
			t.Errorf("banner missing %q:\n%s", want, b)
		}
	}
}

// TestPersistentEnterBanner: the persistent variant additionally names the
// session container and the exit/reattach semantics (no managed multiplexer
// — tmux/zellij are opt-in inside the image).
func TestPersistentEnterBanner(t *testing.T) {
	b := persistentEnterBanner("feat", "podman", "/p/.sandboxer/feat", "sandboxer-feat-cafe0123")
	for _, want := range []string{
		"sandboxer-feat-cafe0123", `"feat"`, "podman", "/p/.sandboxer/feat",
		"keeps the container running", "sandboxer enter feat", "tmux/zellij",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("banner missing %q:\n%s", want, b)
		}
	}
}
