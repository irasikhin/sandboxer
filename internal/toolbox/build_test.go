package toolbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderAgentsNix(t *testing.T) {
	got := renderAgentsNix([]string{"gemini-cli", "claude-code", "aider"})
	// Sorted, one quoted name per line, inside a nix list literal.
	want := "[\n  \"aider\"\n  \"claude-code\"\n  \"gemini-cli\"\n]\n"
	if got != want {
		t.Errorf("renderAgentsNix =\n%q\nwant\n%q", got, want)
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
	if joined == "" {
		t.Error("expected at least one image agent")
	}
}

func TestBuilderArgv(t *testing.T) {
	o := BuildOpts{Engine: "podman", NixImage: "docker.io/nixos/nix:2.31.2"}
	got := builderArgv(o, "/ctx", "/out", "")
	s := strings.Join(got, " ")
	for _, want := range []string{
		"run --rm --name " + builderName,
		"--volume /ctx:/src:ro",
		"--volume /out:/out:rw",
		"docker.io/nixos/nix:2.31.2 sh -lc",
		"path:/src#image",
		"--accept-flake-config",
		"--no-write-lock-file",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("builderArgv missing %q in:\n%s", want, s)
		}
	}
	// No cache volume unless requested.
	if strings.Contains(s, ":/nix") {
		t.Errorf("default build should not mount a /nix cache volume:\n%s", s)
	}

	// --cache adds the named nix-store volume.
	withCache := strings.Join(builderArgv(o, "/ctx", "/out", cacheVolume), " ")
	if !strings.Contains(withCache, "--volume "+cacheVolume+":/nix") {
		t.Errorf("--cache should mount %s:/nix, got:\n%s", cacheVolume, withCache)
	}

	// ExtraArgs (escape hatch) land before the builder image.
	withExtra := builderArgv(BuildOpts{Engine: "podman", NixImage: "n", ExtraArgs: []string{"--security-opt", "seccomp=unconfined"}}, "/ctx", "/out", "")
	imgIdx, soIdx := indexOf(withExtra, "n"), indexOf(withExtra, "seccomp=unconfined")
	if soIdx < 0 || imgIdx < 0 || soIdx > imgIdx {
		t.Errorf("extra args must precede the builder image: %v", withExtra)
	}

	// --refresh threads through to the in-container build command.
	if !strings.Contains(strings.Join(builderArgv(BuildOpts{Engine: "podman", NixImage: "n", Refresh: true}, "/c", "/o", ""), " "), "--refresh") {
		t.Error("--refresh should add --refresh to the build command")
	}
}

func TestLoadArgv(t *testing.T) {
	got := strings.Join(loadArgv("/out/image.tar.gz"), " ")
	if got != "load -i /out/image.tar.gz" {
		t.Errorf("loadArgv = %q", got)
	}
}

func TestWriteContext(t *testing.T) {
	dir := t.TempDir()
	if err := writeContext(dir); err != nil {
		t.Fatalf("writeContext: %v", err)
	}
	for _, f := range []string{"flake.nix", "agents.nix", "sandboxer"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing %s in context: %v", f, err)
		}
	}
	agents, _ := os.ReadFile(filepath.Join(dir, "agents.nix"))
	if !strings.Contains(string(agents), `"claude-code"`) {
		t.Errorf("agents.nix should list claude-code, got:\n%s", agents)
	}
}

// TestBuildImageFakeEngine drives BuildImage with a fake engine that logs its
// argv and exits 0 for everything, so the orchestration (run → load → cleanup)
// is asserted without a real nix build.
func TestBuildImageFakeEngine(t *testing.T) {
	requireExec(t, "sh")
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	engine := writeFakeEngine(t, dir, logPath)

	err := BuildImage(BuildOpts{
		Engine: engine, Image: builtName, NixImage: "nixos/nix:test",
		Stdout: &strings.Builder{}, Stderr: &strings.Builder{},
	})
	if err != nil {
		t.Fatalf("BuildImage: %v", err)
	}
	log := readFile(t, logPath)
	for _, want := range []string{"run --rm", "load -i"} {
		if !strings.Contains(log, want) {
			t.Errorf("engine log missing %q:\n%s", want, log)
		}
	}

	// Custom tag → retag from the built default name.
	logPath2 := filepath.Join(dir, "calls2.log")
	engine2 := writeFakeEngine(t, dir, logPath2)
	if err := BuildImage(BuildOpts{
		Engine: engine2, Image: "myorg/toolbox:dev", NixImage: "nixos/nix:test",
		Stdout: &strings.Builder{}, Stderr: &strings.Builder{},
	}); err != nil {
		t.Fatalf("BuildImage (retag): %v", err)
	}
	if l := readFile(t, logPath2); !strings.Contains(l, "tag "+builtName+" myorg/toolbox:dev") {
		t.Errorf("retag not issued:\n%s", l)
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

func TestBuildImageNoEngine(t *testing.T) {
	if err := BuildImage(BuildOpts{Engine: ""}); err == nil {
		t.Error("empty engine should error")
	}
}

func TestWriteContextError(t *testing.T) {
	if err := writeContext(filepath.Join(t.TempDir(), "missing", "ctx")); err == nil {
		t.Error("writeContext into a nonexistent dir should error")
	}
}

// TestBuildImageBranches covers the pulled-builder cleanup and the run/load
// failure paths via fake engines that exit selectively per subcommand.
func TestBuildImageBranches(t *testing.T) {
	requireExec(t, "sh")
	dir := t.TempDir()
	mk := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// `image inspect` fails ⇒ builder treated as pulled ⇒ removed afterward.
	logP := filepath.Join(dir, "pulled.log")
	eng := mk("eng-pulled", "echo \"$@\" >> "+logP+"\ncase \"$1\" in image) exit 1;; esac\nexit 0\n")
	if err := BuildImage(BuildOpts{Engine: eng, Stdout: &strings.Builder{}, Stderr: &strings.Builder{}}); err != nil {
		t.Fatalf("BuildImage (pulled): %v", err)
	}
	if l := readFile(t, logP); !strings.Contains(l, "rmi "+NixImage) {
		t.Errorf("pulled builder image should be removed:\n%s", l)
	}

	// `run` fails ⇒ build error.
	runFail := mk("eng-runfail", "case \"$1\" in run) exit 2;; esac\nexit 0\n")
	if err := BuildImage(BuildOpts{Engine: runFail, NixImage: "x", Stderr: &strings.Builder{}}); err == nil {
		t.Error("build run failure should error")
	}

	// `load` fails ⇒ load error.
	loadFail := mk("eng-loadfail", "case \"$1\" in load) exit 3;; esac\nexit 0\n")
	if err := BuildImage(BuildOpts{Engine: loadFail, NixImage: "x", Stderr: &strings.Builder{}}); err == nil {
		t.Error("load failure should error")
	}

	// KeepBuilder keeps a pulled builder image; nil Stderr exercises the
	// io.Discard progress path.
	logK := filepath.Join(dir, "keep.log")
	engK := mk("eng-keep", "echo \"$@\" >> "+logK+"\ncase \"$1\" in image) exit 1;; esac\nexit 0\n")
	if err := BuildImage(BuildOpts{Engine: engK, KeepBuilder: true}); err != nil {
		t.Fatalf("BuildImage (keep): %v", err)
	}
	if l := readFile(t, logK); strings.Contains(l, "rmi "+NixImage) {
		t.Errorf("KeepBuilder must not remove the builder image:\n%s", l)
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

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func requireExec(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		if _, err := exec.LookPath(n); err != nil {
			t.Skipf("requires %q on PATH", n)
		}
	}
}

func writeFakeEngine(t *testing.T, dir, logPath string) string {
	t.Helper()
	p := filepath.Join(dir, "engine-"+strings.ReplaceAll(filepath.Base(logPath), ".", "_"))
	// Logs the full argv; `image inspect` exits 0 so the builder is treated as
	// already present (no rmi). Everything else exits 0.
	script := "#!/bin/sh\necho \"$@\" >> " + logPath + "\nexit 0\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
