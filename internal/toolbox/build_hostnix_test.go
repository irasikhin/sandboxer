package toolbox

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeHostNix is a nix stand-in for the host-nix image build: it drops a
// stand-in image tar at FAKE_TAR and prints that path as `--print-out-paths`
// would, so BuildImageHostNix's orchestration (pins → nix build → copy) runs
// without nix or a real build. /bin/sh is an absolute path, so it runs with any
// PATH.
const fakeHostNix = `#!/bin/sh
printf 'IMAGE-TAR' > "$FAKE_TAR"
echo "$FAKE_TAR"
exit 0
`

// TestBuildImageHostNix pins the host-nix build orchestration end to end (fake
// nix): the built tar lands at DestTar, warm pins are reused (no git), and the
// failure modes (missing options, no nix, a failing nix run) are all clear
// errors.
func TestBuildImageHostNix(t *testing.T) {
	warmPins(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "nix")
	if err := os.WriteFile(bin, []byte(fakeHostNix), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_TAR", filepath.Join(dir, "built.tar"))
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	dest := filepath.Join(dir, "toolbox.tar")
	if err := BuildImageHostNix(BuildHostNixOpts{DestTar: dest}); err != nil {
		t.Fatalf("BuildImageHostNix: %v", err)
	}
	data, err := os.ReadFile(dest)
	if err != nil || string(data) != "IMAGE-TAR" {
		t.Errorf("built tar = %q, %v; want IMAGE-TAR at DestTar", data, err)
	}

	// Missing required options fail loudly.
	if err := BuildImageHostNix(BuildHostNixOpts{}); err == nil {
		t.Error("BuildImageHostNix with no DestTar must error")
	}
	// No nix on PATH is a clear install hint, never a bare exec error.
	t.Setenv("PATH", t.TempDir())
	if err := BuildImageHostNix(BuildHostNixOpts{DestTar: dest}); err == nil ||
		!strings.Contains(err.Error(), "nix is not installed") {
		t.Errorf("no nix = %v, want the install hint", err)
	}
	// A failing nix run errors.
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(bin)+":"+os.Getenv("PATH"))
	if err := BuildImageHostNix(BuildHostNixOpts{DestTar: dest}); err == nil ||
		!strings.Contains(err.Error(), "failed") {
		t.Errorf("failing nix = %v, want a build failure", err)
	}
}

// TestHostNixArgv pins the nix argv: the exact reviewer-approved invocation
// (experimental features, accept-flake-config, build, no-link, print-out-paths)
// for path:<ctx>#image — image only, no proxyImage. Tracking revs add no
// override (nothing concrete to point at).
func TestHostNixArgv(t *testing.T) {
	got := hostNixArgv("/ctx", Spec{})
	want := []string{
		"--extra-experimental-features", "nix-command flakes",
		"--accept-flake-config",
		"build", "--no-link", "--print-out-paths", "path:/ctx#image",
	}
	if !slices.Equal(got, want) {
		t.Errorf("hostNixArgv =\n%q\nwant\n%q", got, want)
	}
}

// TestHostNixArgvOverridesPinnedRevs pins the fix for the stale-agents trap:
// writeContext copies the embedded flake verbatim, so the spec's resolved revs
// MUST reach nix as --override-input — without them every build silently
// reproduced the embedded snapshot (guests stayed on a months-old claude-code
// no matter how often `image build` re-stamped the pins cache).
func TestHostNixArgvOverridesPinnedRevs(t *testing.T) {
	nixRev := strings.Repeat("a", 40)
	agentsRev := strings.Repeat("b", 40)
	got := hostNixArgv("/ctx", Spec{NixpkgsRev: nixRev, LLMAgentsRev: agentsRev})
	want := []string{
		"--extra-experimental-features", "nix-command flakes",
		"--accept-flake-config",
		"build", "--no-link", "--print-out-paths",
		"--override-input", "nixpkgs", "github:NixOS/nixpkgs/" + nixRev,
		"--override-input", "llm-agents", "github:numtide/llm-agents.nix/" + agentsRev,
		"path:/ctx#image",
	}
	if !slices.Equal(got, want) {
		t.Errorf("hostNixArgv(pinned) =\n%q\nwant\n%q", got, want)
	}
}

// TestBuildImageHostNixMultiLineOutput pins the --print-out-paths parse: a
// multi-output derivation prints one path per line, and the build must resolve
// to the LAST non-empty line rather than failing on a path with an embedded
// newline. The fake nix prints a junk first line then the real store path.
func TestBuildImageHostNixMultiLineOutput(t *testing.T) {
	warmPins(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "nix")
	real := filepath.Join(dir, "built.tar")
	script := "#!/bin/sh\n" +
		"printf 'IMAGE-TAR' > \"" + real + "\"\n" +
		"printf '/nix/store/aaaa-other-output\\n%s\\n' \"" + real + "\"\n" +
		"exit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	dest := filepath.Join(dir, "toolbox.tar")
	if err := BuildImageHostNix(BuildHostNixOpts{DestTar: dest}); err != nil {
		t.Fatalf("BuildImageHostNix (multi-line): %v", err)
	}
	data, err := os.ReadFile(dest)
	if err != nil || string(data) != "IMAGE-TAR" {
		t.Errorf("built tar = %q, %v; want IMAGE-TAR from the last printed line", data, err)
	}
}
