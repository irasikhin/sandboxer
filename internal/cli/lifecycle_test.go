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
// session container and the detach/reattach semantics.
func TestPersistentEnterBanner(t *testing.T) {
	b := persistentEnterBanner("feat", "podman", "/p/.sandboxer/feat", "sandboxer-feat-cafe0123")
	for _, want := range []string{
		"sandboxer-feat-cafe0123", `"feat"`, "podman", "/p/.sandboxer/feat",
		"Ctrl-q", "session keeps running", "sandboxer enter feat",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("banner missing %q:\n%s", want, b)
		}
	}
}

// TestTmuxEnterArgs pins the guarded tmux launcher: attach-or-create on the
// shared sandboxer tmux server under the shipped config, with the plain-shell
// fallback (and rebuild hint) for an older image without tmux.
func TestTmuxEnterArgs(t *testing.T) {
	got := tmuxEnterArgs("main")
	if len(got) != 3 || got[0] != "bash" || got[1] != "-c" {
		t.Fatalf("want [bash -c <script>], got %v", got)
	}
	script := got[2]
	for _, want := range []string{
		"command -v tmux",
		"tmux -L sandboxer -f /etc/sandboxer/tmux.conf new-session -A -s main",
		"rebuild: sandboxer image build",
		"--rcfile", "exec bash -i",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("launcher missing %q in %q", want, script)
		}
	}
	if !strings.Contains(tmuxEnterArgs("review")[2], "-s review") {
		t.Error("session name not interpolated")
	}
}

// TestValidateSessionName: the name lands inside a bash -c word, so only the
// safe alphabet passes.
func TestValidateSessionName(t *testing.T) {
	for _, ok := range []string{"main", "a-b_c", "X1", "0"} {
		if err := validateSessionName(ok); err != nil {
			t.Errorf("validateSessionName(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "a b", "a;b", "a/b", "$(x)", "a'b", "ä"} {
		if err := validateSessionName(bad); err == nil {
			t.Errorf("validateSessionName(%q) = nil, want error", bad)
		}
	}
}
