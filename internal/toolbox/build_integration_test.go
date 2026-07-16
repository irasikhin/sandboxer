//go:build integration

package toolbox_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/itest"
	"github.com/irasikhin/sandboxer/internal/toolbox"
)

// TestBuildImageUserNixContract drives a REAL variant build end to end: a tiny
// user image.nix exercising all three phase-2 keys (packages, files, env) is
// built by the actual nixos/nix builder container and the result is probed by
// running it. This is the only test that evaluates the embedded flake's user
// contract for real — the unit suite only asserts substrings of the asset —
// so a nix-level error in that block surfaces here, not at a user's first
// build. Gated like itest.EnsureToolboxImage: the build is multi-minute, so it
// runs only with SANDBOXER_ITEST_BUILD_IMAGE=1.
func TestBuildImageUserNixContract(t *testing.T) {
	engine := itest.Engine(t)
	if os.Getenv("SANDBOXER_ITEST_BUILD_IMAGE") != "1" {
		t.Skip("real nix-in-container image build (multi-minute) — set SANDBOXER_ITEST_BUILD_IMAGE=1 to run")
	}

	nixFile := filepath.Join(t.TempDir(), "image.nix")
	hook := `{ pkgs }:
{
  packages = [ pkgs.hello ];
  files."/etc/sandboxer-itest" = "user-nix-ok\n";
  env.SANDBOXER_ITEST_FLAG = "from-user-nix";
}
`
	if err := os.WriteFile(nixFile, []byte(hook), 0o644); err != nil {
		t.Fatal(err)
	}
	spec, err := toolbox.ResolveSpec(&config.Profile{Image: config.ImageSpec{Overlay: nixFile}})
	if err != nil {
		t.Fatalf("ResolveSpec: %v", err)
	}
	tag := spec.Tag()
	t.Cleanup(func() { _ = exec.Command(engine, "rmi", tag).Run() })

	if err := toolbox.BuildImage(toolbox.BuildOpts{
		Engine: engine, Image: tag, Spec: spec,
		Stdout: os.Stderr, Stderr: os.Stderr,
	}); err != nil {
		t.Fatalf("variant build: %v", err)
	}

	probe := func(args ...string) string {
		t.Helper()
		out, err := exec.Command(engine, append([]string{"run", "--rm", tag}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("probe %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	if got := probe("cat", "/etc/sandboxer-itest"); !strings.Contains(got, "user-nix-ok") {
		t.Errorf("files.\"/etc/sandboxer-itest\" not baked in: %q", got)
	}
	if got := probe("sh", "-lc", "echo $SANDBOXER_ITEST_FLAG"); !strings.Contains(got, "from-user-nix") {
		t.Errorf("env.SANDBOXER_ITEST_FLAG not baked in: %q", got)
	}
	if got := probe("hello"); !strings.Contains(strings.ToLower(got), "hello") {
		t.Errorf("packages = [ pkgs.hello ] not baked in: %q", got)
	}
}
