package cli

import (
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
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

// TestOneShotEnterBanner: the one-shot banner must say the OPPOSITE of the
// persistent one — in a `run --rm` container tmux is the main process, so a
// detach ends it. It also has to reassure that the work is not lost, and offer
// the way back only when a persistent session was set aside (an ephemeral run
// is what the user asked for).
func TestOneShotEnterBanner(t *testing.T) {
	stale := oneShotEnterBanner("feat", "podman", "/p/feat", "session sandboxer-feat-cafe is stale (image rebuilt)", true)
	for _, want := range []string{
		`"feat"`, "podman", "/p/feat", "one-shot", "image rebuilt",
		"ENDS it", "survives", "sandboxer stop feat && sandboxer enter feat", "--recreate",
	} {
		if !strings.Contains(stale, want) {
			t.Errorf("stale-fallback banner missing %q:\n%s", want, stale)
		}
	}
	if strings.Contains(stale, "keeps the container running") {
		t.Errorf("one-shot banner promises persistence:\n%s", stale)
	}

	eph := oneShotEnterBanner("feat", "podman", "/p/feat", "ephemeral mode (--ephemeral)", false)
	if !strings.Contains(eph, "ENDS it") || strings.Contains(eph, "sandboxer stop") {
		t.Errorf("a deliberate ephemeral run should warn but not offer a way back:\n%s", eph)
	}
}

// TestEphemeralWhy: a one-shot container must name WHICH switch caused it, in
// ResolveRuntime's precedence order — an exported SANDBOXER_SESSION outranks
// the profile, and that is exactly the case a user cannot see from the config.
func TestEphemeralWhy(t *testing.T) {
	t.Setenv("SANDBOXER_SESSION", "")
	if got := ephemeralWhy(commonFlags{ephemeral: true}, nil); !strings.Contains(got, "--ephemeral") {
		t.Errorf("flag: %q", got)
	}
	prof := &config.Profile{Session: config.SessionEphemeral}
	if got := ephemeralWhy(commonFlags{}, prof); !strings.Contains(got, "profile") {
		t.Errorf("profile: %q", got)
	}
	t.Setenv("SANDBOXER_SESSION", config.SessionEphemeral)
	if got := ephemeralWhy(commonFlags{}, prof); !strings.Contains(got, "SANDBOXER_SESSION") {
		t.Errorf("env must outrank the profile: %q", got)
	}
}

// TestPersistentEnterBanner: the persistent variant names the session
// container AND keeps detach and exit apart. They are not equivalent — exiting
// the shell closes the last pane, which ends the tmux session (and with
// exit-empty the server), while only the container survives. Saying just
// "exiting keeps the container running" read as "exiting is safe" and cost a
// user their session.
func TestPersistentEnterBanner(t *testing.T) {
	b := persistentEnterBanner("feat", "podman", "/p/.sandboxer/feat", "sandboxer-feat-cafe0123")
	for _, want := range []string{
		"sandboxer-feat-cafe0123", `"feat"`, "podman", "/p/.sandboxer/feat",
		"sandboxer enter feat", "Ctrl-Space d", "DETACHES", "ENDS that tmux session",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("banner missing %q:\n%s", want, b)
		}
	}
	if strings.Contains(b, "exiting keeps the container running") {
		t.Errorf("the banner still implies exiting preserves the session:\n%s", b)
	}
}
