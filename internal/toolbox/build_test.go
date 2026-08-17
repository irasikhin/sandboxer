package toolbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderNixList(t *testing.T) {
	got := renderNixList([]string{"gemini-cli", "claude-code", "crush"})
	// Sorted, one quoted name per line, inside a nix list literal.
	want := "[\n  \"claude-code\"\n  \"crush\"\n  \"gemini-cli\"\n]\n"
	if got != want {
		t.Errorf("renderNixList =\n%q\nwant\n%q", got, want)
	}
}

func TestImageAgentPackages(t *testing.T) {
	pkgs := imageAgentPackages()
	joined := strings.Join(pkgs, ",")
	// claude is the default, always baked.
	if !contains(pkgs, "claude-code") {
		t.Errorf("expected claude-code in image agents, got %v", pkgs)
	}
	// codex is image:false → must be excluded.
	if contains(pkgs, "codex") {
		t.Errorf("codex (image:false) must be excluded, got %v", pkgs)
	}
	// The agents nixpkgs does not carry are vendored beside the embedded flake
	// and grafted into pkgs by its overlay — they must be in the baked list
	// like any other, or the vendoring buys nothing.
	for _, want := range []string{"pi", "dsh"} {
		if !contains(pkgs, want) {
			t.Errorf("expected vendored agent %q in image agents, got %v", want, pkgs)
		}
	}
	if joined == "" {
		t.Error("expected at least one image agent")
	}
}

func TestWriteContext(t *testing.T) {
	dir := t.TempDir()
	if err := writeContext(dir, Spec{Attrs: []string{"nodejs", "go"}}); err != nil {
		t.Fatalf("writeContext: %v", err)
	}
	files := []string{"flake.nix", "images.nix", "agents.nix", "tools.nix", "overlay.nix", "files.json", "env.json"}
	// The vendored packages the embedded flake's overlay grafts in (none of
	// them is in nixpkgs): each contributes its expression AND its lockfile,
	// and a missing one fails the build inside the builder, far from here.
	for _, dir := range vendored {
		files = append(files, dir+"/package.nix", dir+"/package-lock.json")
	}
	// dsh contributes more than the pair — its launcher script and the web
	// bind overlay the script injects — so the context copies whatever the
	// package dir holds rather than a hand-listed pair.
	files = append(files, "dsh/dsh-launch.sh", "dsh/web-bind.patch.yml")
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f))); err != nil {
			t.Errorf("missing %s in context: %v", f, err)
		}
	}
	// The sandboxer binary is NOT copied into the image context — it is a
	// host tool, kept out of the toolbox image.
	if _, err := os.Stat(filepath.Join(dir, "sandboxer")); !os.IsNotExist(err) {
		t.Errorf("sandboxer binary must not be in the image context (err=%v)", err)
	}
	agents, _ := os.ReadFile(filepath.Join(dir, "agents.nix"))
	if !strings.Contains(string(agents), `"claude-code"`) {
		t.Errorf("agents.nix should list claude-code, got:\n%s", agents)
	}
	tools, _ := os.ReadFile(filepath.Join(dir, "tools.nix"))
	if !strings.Contains(string(tools), `"go"`) || !strings.Contains(string(tools), `"nodejs"`) {
		t.Errorf("tools.nix should list the tool attrs, got:\n%s", tools)
	}
	// No OverlayFile → overlay.nix is the no-op stub (the flake import is
	// unconditional).
	if got := readFile(t, filepath.Join(dir, "overlay.nix")); got != stubOverlay {
		t.Errorf("overlay.nix stub = %q, want %q", got, stubOverlay)
	}
}

// TestWriteContextOverlayAndData: a spec's overlay file is copied verbatim
// into the context, files/env render as JSON, and a missing overlay file
// fails the context assembly.
func TestWriteContextUserNix(t *testing.T) {
	src := filepath.Join(t.TempDir(), "overlay.nix")
	body := "final: prev: { greet = prev.hello; }\n"
	if err := os.WriteFile(src, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	spec := Spec{
		OverlayFile: src,
		Files:       map[string]string{"/etc/x.conf": "line\n"},
		Env:         map[string]string{"FOO": "bar"},
	}
	if err := writeContext(dir, spec); err != nil {
		t.Fatalf("writeContext: %v", err)
	}
	if got := readFile(t, filepath.Join(dir, "overlay.nix")); got != body {
		t.Errorf("overlay.nix = %q, want the overlay file copied verbatim", got)
	}
	if got := readFile(t, filepath.Join(dir, "files.json")); got != `{"/etc/x.conf":"line\n"}` {
		t.Errorf("files.json = %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "env.json")); got != `{"FOO":"bar"}` {
		t.Errorf("env.json = %q", got)
	}
	// No customization → the no-op overlay stub and empty JSON objects.
	plain := t.TempDir()
	if err := writeContext(plain, Spec{}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(plain, "overlay.nix")); got != stubOverlay {
		t.Errorf("stub overlay = %q", got)
	}
	if got := readFile(t, filepath.Join(plain, "files.json")); got != "{}" {
		t.Errorf("empty files.json = %q", got)
	}

	err := writeContext(t.TempDir(), Spec{OverlayFile: filepath.Join(t.TempDir(), "missing.nix")})
	if err == nil || !strings.Contains(err.Error(), "image.overlay") {
		t.Errorf("missing overlay file should fail context assembly, got %v", err)
	}
}

// warmPins isolates the pins cache and stamps both inputs, so a build test
// never resolves against the network — the internal PinSpec fills the
// tracking revs from the stamp.
func warmPins(t *testing.T) {
	t.Helper()
	pinsCacheDir(t)
	if err := SavePins(Pins{
		"nixpkgs": {Ref: "refs/heads/nixos-unstable", Rev: strings.Repeat("d", 40)},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst")
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	if got := readFile(t, dst); got != "hello" {
		t.Errorf("copied content = %q", got)
	}
	if err := copyFile(filepath.Join(dir, "nope"), dst); err == nil {
		t.Error("missing src should error")
	}
	if err := copyFile(src, filepath.Join(dir, "no", "such", "dir", "x")); err == nil {
		t.Error("unwritable dst should error")
	}
	// Opening succeeds but reading a directory fails mid-copy.
	if err := copyFile(dir, filepath.Join(dir, "d2")); err == nil {
		t.Error("copying a directory as a file should error")
	}
}

func TestWriteContextError(t *testing.T) {
	if err := writeContext(filepath.Join(t.TempDir(), "missing", "ctx"), Spec{}); err == nil {
		t.Error("writeContext into a nonexistent dir should error")
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func requireExec(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		if _, err := exec.LookPath(n); err != nil {
			t.Skipf("requires %q on PATH", n)
		}
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
