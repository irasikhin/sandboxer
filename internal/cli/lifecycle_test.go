package cli

import (
	"strings"
	"testing"
)

// TestTmuxEnterArgs pins the guarded launcher: bash -c attaching the named
// session on the sandboxer tmux socket, with the plain rc-shell fallback (and
// its rebuild hint) for images from before tmux was baked in.
func TestTmuxEnterArgs(t *testing.T) {
	got := tmuxEnterArgs("main")
	if len(got) != 3 || got[0] != "bash" || got[1] != "-c" {
		t.Fatalf("want [bash -c <script>], got %v", got)
	}
	script := got[2]
	for _, want := range []string{
		"tmux -L sandboxer new-session -A -s main",
		"sandboxer image build", // the older-image hint
		"/etc/sandboxer/rc.sh", "--rcfile", "exec bash -i",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("launcher missing %q in %q", want, script)
		}
	}
}

// TestValidateSessionName: the name is spliced into a bash -c script, so
// anything outside the safe alphabet must be rejected up front.
func TestValidateSessionName(t *testing.T) {
	for _, ok := range []string{"main", "side-2", "A_b"} {
		if err := validateSessionName(ok); err != nil {
			t.Errorf("validateSessionName(%q) = %v, want ok", ok, err)
		}
	}
	for _, bad := range []string{"", "a b", "x;rm -rf /", "$(id)", "a'b"} {
		if err := validateSessionName(bad); err == nil {
			t.Errorf("validateSessionName(%q) accepted an unsafe name", bad)
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
// session container and the detach/exit/reattach semantics of the attached
// tmux.
func TestPersistentEnterBanner(t *testing.T) {
	b := persistentEnterBanner("feat", "podman", "/p/.sandboxer/feat", "sandboxer-feat-cafe0123")
	for _, want := range []string{
		"sandboxer-feat-cafe0123", `"feat"`, "podman", "/p/.sandboxer/feat",
		"keeps the container running", "sandboxer enter feat", "Ctrl-b d",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("banner missing %q:\n%s", want, b)
		}
	}
}
