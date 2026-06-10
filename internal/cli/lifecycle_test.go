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

// TestEnterBanner checks the orientation notice carries the slug, engine, dir
// and the non-obvious exit-pushes-deps semantics.
func TestEnterBanner(t *testing.T) {
	b := enterBanner("feat", "podman", "/p/.sandboxer/feat")
	for _, want := range []string{`"feat"`, "podman", "/p/.sandboxer/feat", "pushes rw deps back"} {
		if !strings.Contains(b, want) {
			t.Errorf("banner missing %q:\n%s", want, b)
		}
	}
}
