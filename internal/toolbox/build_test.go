package toolbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
)

func TestRenderNixList(t *testing.T) {
	got := renderNixList([]string{"gemini-cli", "claude-code", "aider"})
	// Sorted, one quoted name per line, inside a nix list literal.
	want := "[\n  \"aider\"\n  \"claude-code\"\n  \"gemini-cli\"\n]\n"
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
	if joined == "" {
		t.Error("expected at least one image agent")
	}
}

func TestBuilderArgv(t *testing.T) {
	clearProxyEnv(t) // a developer's own proxy must not change the expected argv
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
}

// TestBuilderScriptOverrides pins the --override-input emission rules: no
// override (or one equal to the embedded pin) keeps the script byte-identical
// to a stock build; a differing rev adds exactly the matching override flag.
func TestBuilderScriptOverrides(t *testing.T) {
	embNixpkgs, embLLMAgents := EmbeddedRevs()
	stock := builderScript(embNixpkgs, embLLMAgents)
	if strings.Contains(stock, "--override-input") {
		t.Errorf("revs equal to the embedded pins must carry no overrides:\n%s", stock)
	}
	revA, revB := strings.Repeat("a", 40), strings.Repeat("b", 40)
	with := builderScript(revA, revB)
	for _, want := range []string{
		"--override-input nixpkgs github:NixOS/nixpkgs/" + revA,
		"--override-input llm-agents github:numtide/llm-agents.nix/" + revB,
	} {
		if !strings.Contains(with, want) {
			t.Errorf("override script missing %q:\n%s", want, with)
		}
	}
	// One differing, one embedded → only the differing input gets a flag.
	one := builderScript(embNixpkgs, revB)
	if strings.Contains(one, "--override-input nixpkgs") || !strings.Contains(one, "--override-input llm-agents") {
		t.Errorf("expected only the llm-agents override:\n%s", one)
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
	if err := writeContext(dir, Spec{Attrs: []string{"nodejs", "go"}}); err != nil {
		t.Fatalf("writeContext: %v", err)
	}
	for _, f := range []string{"flake.nix", "images.nix", "agents.nix", "tools.nix", "overlay.nix", "files.json", "env.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing %s in context: %v", f, err)
		}
	}
	// The sandboxer binary is NOT copied into the image context anymore — it is a
	// host tool, kept out of the toolbox image (egress is a squid sidecar).
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

// warmPins isolates the pins cache and stamps both inputs, so a BuildImage
// test never resolves against its fake engine (which cannot answer the
// resolver) — the internal PinSpec fills the tracking revs from the stamp.
func warmPins(t *testing.T) {
	t.Helper()
	pinsCacheDir(t)
	if err := SavePins(Pins{
		"nixpkgs":    {Ref: "refs/heads/nixos-unstable", Rev: strings.Repeat("d", 40)},
		"llm-agents": {Ref: "HEAD", Rev: strings.Repeat("e", 40)},
	}); err != nil {
		t.Fatal(err)
	}
}

// TestBuildImageFakeEngine drives BuildImage with a fake engine that logs its
// argv and exits 0 for everything, so the orchestration (run → load → cleanup)
// is asserted without a real nix build.
func TestBuildImageFakeEngine(t *testing.T) {
	requireExec(t, "sh")
	warmPins(t)
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

	// Variant build: a non-empty spec under its content-addressed var- tag is
	// retagged from the built default name like any custom tag. The spec is
	// pinned first, as every Tag() caller must be.
	spec, err := PinSpec(Spec{Attrs: []string{"ripgrep"}}, "", "", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	variant := spec.Tag()
	if !strings.HasPrefix(variant, "sandboxer-toolbox:var-") {
		t.Fatalf("unexpected variant tag %q", variant)
	}
	logPath3 := filepath.Join(dir, "calls3.log")
	engine3 := writeFakeEngine(t, dir, logPath3)
	if err := BuildImage(BuildOpts{
		Engine: engine3, Image: variant, NixImage: "nixos/nix:test", Spec: spec,
		Stdout: &strings.Builder{}, Stderr: &strings.Builder{},
	}); err != nil {
		t.Fatalf("BuildImage (variant): %v", err)
	}
	if l := readFile(t, logPath3); !strings.Contains(l, "tag "+builtName+" "+variant) {
		t.Errorf("variant retag not issued:\n%s", l)
	}
}

// TestBuildImageVariantKeepsStockTag: building a variant while the stock
// default image exists must give the stock tag back to the user's previous
// image after the load re-points it — never leave it dangling/tagless (the
// next stock enter would otherwise trigger a full rebuild).
func TestBuildImageVariantKeepsStockTag(t *testing.T) {
	requireExec(t, "sh")
	warmPins(t)
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	eng := filepath.Join(dir, "engine")
	// `image inspect --format {{.Id}} <stock>` answers with the pre-existing
	// stock image's ID; everything else logs and exits 0.
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> " + logPath + "\n" +
		"if [ \"$1\" = image ] && [ \"$3\" = --format ]; then echo sha256:stockid123; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(eng, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	spec, err := PinSpec(Spec{Attrs: []string{"ripgrep"}}, "", "", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	variant := spec.Tag()
	if err := BuildImage(BuildOpts{
		Engine: eng, Image: variant, NixImage: "nixos/nix:test", Spec: spec,
		Stdout: &strings.Builder{}, Stderr: &strings.Builder{},
	}); err != nil {
		t.Fatalf("BuildImage: %v", err)
	}
	log := readFile(t, logPath)
	if !strings.Contains(log, "tag "+builtName+" "+variant) {
		t.Errorf("variant retag not issued:\n%s", log)
	}
	if !strings.Contains(log, "tag stockid123 "+builtName) {
		t.Errorf("stock tag not restored to the previous image:\n%s", log)
	}
	if strings.Contains(log, "rmi "+builtName) {
		t.Errorf("stock tag must be restored, not removed:\n%s", log)
	}
}

// TestBuildImageBuilderPulledFlag: BuilderPulled marks a builder image pulled
// by an earlier pin resolve as ours, so clean-by-default removes it even
// though BuildImage's own probe now sees it as present.
func TestBuildImageBuilderPulledFlag(t *testing.T) {
	requireExec(t, "sh")
	warmPins(t)
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	eng := writeFakeEngine(t, dir, logPath) // image inspect exits 0: "present"
	if err := BuildImage(BuildOpts{
		Engine: eng, NixImage: "nixos/nix:test", BuilderPulled: true,
		Stdout: &strings.Builder{}, Stderr: &strings.Builder{},
	}); err != nil {
		t.Fatalf("BuildImage: %v", err)
	}
	if l := readFile(t, logPath); !strings.Contains(l, "rmi nixos/nix:test") {
		t.Errorf("a resolver-pulled builder image must still be removed:\n%s", l)
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
	if err := writeContext(filepath.Join(t.TempDir(), "missing", "ctx"), Spec{}); err == nil {
		t.Error("writeContext into a nonexistent dir should error")
	}
}

// TestBuildImageBranches covers the pulled-builder cleanup and the run/load
// failure paths via fake engines that exit selectively per subcommand.
func TestBuildImageBranches(t *testing.T) {
	requireExec(t, "sh")
	warmPins(t)
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

// clearProxyEnv unsets every proxy variable HostProxyEnv looks at, so an argv
// assertion does not depend on whether the machine running the tests is behind
// a proxy.
func clearProxyEnv(t *testing.T) {
	t.Helper()
	for _, n := range []string{
		"http_proxy", "https_proxy", "ftp_proxy", "all_proxy", "no_proxy",
		"HTTP_PROXY", "HTTPS_PROXY", "FTP_PROXY", "ALL_PROXY", "NO_PROXY",
	} {
		t.Setenv(n, "")
	}
}

// TestBuilderArgvCarriesHostProxy: the builder is a container and starts with
// an empty environment, so on a proxied machine it could reach nothing —
// including the llm-agents binary cache, which made nix fall back to compiling
// every agent from source and then time out fetching release tarballs. A
// localhost proxy must arrive rewritten to the host gateway (the user means
// "on my host", not the container's own loopback), with the gateway aliases
// mapped so that name resolves, and an explicit --builder-arg must still win.
func TestBuilderArgvCarriesHostProxy(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv("http_proxy", "http://127.0.0.1:8888")
	t.Setenv("no_proxy", "localhost,.corp")

	argv := builderArgv(BuildOpts{Engine: "podman", NixImage: "n"}, "/ctx", "/out", "")
	s := strings.Join(argv, " ")
	for _, want := range []string{
		"--env http_proxy=http://" + config.HostGatewayAlias + ":8888",
		"--env no_proxy=localhost,.corp",
		"--add-host=" + config.HostGatewayAlias + ":host-gateway",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("builderArgv missing %q in:\n%s", want, s)
		}
	}
	// Still before the builder image, like every other run flag.
	if envIdx, imgIdx := indexOf(argv, "http_proxy=http://"+config.HostGatewayAlias+":8888"), indexOf(argv, "n"); envIdx < 0 || envIdx > imgIdx {
		t.Errorf("proxy env must precede the builder image: %v", argv)
	}

	// A user's own --builder-arg is appended last, so it overrides.
	over := builderArgv(BuildOpts{Engine: "podman", NixImage: "n",
		ExtraArgs: []string{"--env", "http_proxy=http://other:1"}}, "/ctx", "/out", "")
	ours, theirs := indexOf(over, "http_proxy=http://"+config.HostGatewayAlias+":8888"), indexOf(over, "http_proxy=http://other:1")
	if theirs < 0 || theirs < ours {
		t.Errorf("--builder-arg must come after the inherited proxy env: %v", over)
	}
}

// TestBuilderArgvCleanWithoutProxy: a machine with no proxy gets exactly the
// argv it got before this feature — no stray --env, no --add-host.
func TestBuilderArgvCleanWithoutProxy(t *testing.T) {
	clearProxyEnv(t)
	s := strings.Join(builderArgv(BuildOpts{Engine: "podman", NixImage: "n"}, "/ctx", "/out", ""), " ")
	if strings.Contains(s, "--env") || strings.Contains(s, "--add-host") {
		t.Errorf("no proxy on the host should leave the argv untouched:\n%s", s)
	}
}

// TestBuilderArgvHostNetworkKeepsLocalhost: with --builder-arg=--network=host
// the container shares the host's netns, so localhost already IS the host. The
// proxy URL must be passed through untouched and the gateway aliases dropped —
// rewriting would point a working SOCKS5/HTTP proxy at the bridge gateway,
// which a loopback-bound proxy does not listen on.
func TestBuilderArgvHostNetworkKeepsLocalhost(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv("all_proxy", "socks5h://127.0.0.1:1080")

	for _, spelling := range [][]string{
		{"--network=host"}, {"--net=host"}, {"--network", "host"}, {"--net", "host"},
	} {
		s := strings.Join(builderArgv(BuildOpts{Engine: "podman", NixImage: "n", ExtraArgs: spelling}, "/c", "/o", ""), " ")
		if !strings.Contains(s, "--env all_proxy=socks5h://127.0.0.1:1080") {
			t.Errorf("%v: proxy was rewritten under host networking:\n%s", spelling, s)
		}
		if strings.Contains(s, "--add-host") {
			t.Errorf("%v: gateway aliases are pointless under host networking:\n%s", spelling, s)
		}
	}

	// Without host networking the rewrite still applies.
	s := strings.Join(builderArgv(BuildOpts{Engine: "podman", NixImage: "n"}, "/c", "/o", ""), " ")
	if !strings.Contains(s, "--env all_proxy=socks5h://"+config.HostGatewayAlias+":1080") {
		t.Errorf("bridge networking should rewrite localhost:\n%s", s)
	}
}
