package cli

import (
	"os"
	"path/filepath"
	"slices"
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

// TestPodmanSocketPrefix pins the exec wrap: every in-guest user command is
// prefixed with the podman-socket ensure (idempotent, NON-fatal — a sandbox
// whose socket cannot come up still runs the command, and the tool that needs
// the socket fails on its own), and the original command re-execs with its
// argv intact (argv0 preserved as bash -c's $0).
func TestPodmanSocketPrefix(t *testing.T) {
	got := podmanSocketPrefix([]string{"npm", "test", "--", "x y"})
	want := []string{"bash", "-c",
		"command -v podman-socket >/dev/null 2>&1 && podman-socket >/dev/null 2>&1 || true; exec \"$0\" \"$@\"",
		"npm", "test", "--", "x y"}
	if !slices.Equal(got, want) {
		t.Errorf("podmanSocketPrefix = %q, want %q", got, want)
	}
	if got := podmanSocketPrefix(nil); got != nil {
		t.Errorf("podmanSocketPrefix(nil) = %q, want nil", got)
	}
}

// TestOneShotEnterBanner: the one-shot banner must say the OPPOSITE of the
// persistent one — in a `run --rm` container tmux is the main process, so a
// detach ends it. It also has to reassure that the work is not lost, and name
// which ephemeral switch chose this shape. Only a deliberately ephemeral run
// reaches it now: a stale persistent session is converged or attached.
func TestOneShotEnterBanner(t *testing.T) {
	eph := oneShotEnterBanner("feat", "podman", "/p/feat", "ephemeral mode (--ephemeral)")
	for _, want := range []string{
		`"feat"`, "podman", "/p/feat", "one-shot", "--ephemeral", "ENDS it", "survives",
	} {
		if !strings.Contains(eph, want) {
			t.Errorf("one-shot banner missing %q:\n%s", want, eph)
		}
	}
	if strings.Contains(eph, "keeps the container running") {
		t.Errorf("one-shot banner promises persistence:\n%s", eph)
	}
}

// TestStaleSessionEnterBanner: attaching to a stale-but-busy session keeps the
// PERSISTENT detach semantics (the user is in their real session), while
// stating that the config change has not landed and the one command that
// applies it — otherwise an edit that silently does nothing is a mystery.
func TestStaleSessionEnterBanner(t *testing.T) {
	b := staleSessionEnterBanner("feat", "podman", "/p/feat", "sandboxer-feat-cafe", "profile changed")
	for _, want := range []string{
		"sandboxer-feat-cafe", `"feat"`, "podman", "/p/feat", "attaching as-is",
		"profile changed", "Ctrl-Space d DETACHES", "sandboxer stop feat && sandboxer enter feat",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("stale-attach banner missing %q:\n%s", want, b)
		}
	}
	if strings.Contains(b, "one-shot") || strings.Contains(b, "--rm") {
		t.Errorf("the stale-attach banner must not describe a one-shot container:\n%s", b)
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

// TestCreateValidatesBackend pins that create refuses the same configs every
// other command refuses. It used to accept them: the warn* helpers ran but
// ValidateBackend did not, so a doomed profile was reported as "created" —
// worktrees and a state dir on disk — and only the first enter said it could
// never have worked.
func TestCreateValidatesBackend(t *testing.T) {
	project := newProject(t)
	cfg := `{
  name = "feat";
  backend = "docker";
  srcs = [ { src = "."; branch = "sbx/feat"; } ];
  hostConfigs = false;
}
`
	if err := os.WriteFile(filepath.Join(project, "sandboxer.nix"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errs := run("create", "feat", "--src", project)
	if code == 0 {
		t.Fatalf("create accepted a container-era backend (exit 0)\nout: %s\nerr: %s", out, errs)
	}
	if !strings.Contains(errs, "container backend was removed") {
		t.Errorf("error should carry the migration hint, got: %s", errs)
	}
	if strings.Contains(out, "created") {
		t.Errorf("create announced success for a rejected profile: %s", out)
	}
}
