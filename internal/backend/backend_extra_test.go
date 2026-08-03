package backend

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/irasikhin/sandboxer/internal/config"
	"github.com/irasikhin/sandboxer/internal/toolbox"
)

// TestContainerRunWithEgress exercises the egress-allowlist branch of Run: the
// fake engine succeeds for every network/run/connect call, so egress.Up brings
// the sidecar up and Run wires the proxy env and --network flags.
func TestContainerRunWithEgress(t *testing.T) {
	requireExec(t, "sh")
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	engine := filepath.Join(dir, "engine")
	writeEngineScript(t, engine, logPath) // exits 0 for all subcommands

	code, err := Run(RunOpts{
		MountDest: true, Engine: engine, Image: "toolbox:latest", Dest: t.TempDir(), Slug: "s",
		RT:       config.Runtime{Egress: true, Domains: []string{"a.com"}},
		NoEgress: false, Args: []string{"true"},
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
	})
	if err != nil || code != 0 {
		t.Fatalf("Run with egress = (%d,%v)", code, err)
	}
	log, _ := os.ReadFile(logPath)
	s := string(log)
	// The squid sidecar was created (with the generated allowlist config) and the
	// agent run joined its network with proxy env.
	for _, want := range []string{"network create --internal", "/etc/sandboxer/squid.conf:ro", "sandboxer-proxy", "--network ", "HTTP_PROXY=http://"} {
		if !strings.Contains(s, want) {
			t.Errorf("egress Run missing %q in:\n%s", want, s)
		}
	}
	// Teardown ran on the deferred Down().
	if !strings.Contains(s, "network rm") {
		t.Errorf("egress teardown not invoked:\n%s", s)
	}
}

// TestContainerRunEgressFailRefuses: when the allowlist is required but the
// sidecar cannot start, Run fails closed — it errors and never launches the
// agent on an open bridge network.
func TestContainerRunEgressFailRefuses(t *testing.T) {
	requireExec(t, "sh")
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	engine := filepath.Join(dir, "engine")
	writeEngineScript(t, engine, logPath)
	t.Setenv("SBX_EXIT", "1") // every engine call fails, including `network create`

	code, err := Run(RunOpts{
		MountDest: true, Engine: engine, Image: "img", Dest: t.TempDir(), Slug: "s",
		RT:       config.Runtime{Egress: true, Domains: []string{"a.com"}},
		NoEgress: false, Args: []string{"true"},
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
	})
	if err == nil {
		t.Fatal("egress required but proxy failed should error (no bridge fallback)")
	}
	if code != 0 {
		t.Errorf("failed-egress exit code = %d, want 0 alongside the error", code)
	}
	log, _ := os.ReadFile(logPath)
	if strings.Contains(string(log), "run --rm") {
		t.Errorf("agent container ran despite egress failure:\n%s", log)
	}
}

// TestContainerProxyURL pins the localhost→host-gateway rewrite: a proxy a user
// runs "on localhost" is on the host, not the container's own loopback, so it is
// rewritten to host.docker.internal; any other host (and unparseable/empty
// input) is left untouched.
func TestContainerProxyURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"http://localhost:9999", "http://host.docker.internal:9999"},
		{"http://127.0.0.1:3128", "http://host.docker.internal:3128"},
		{"http://[::1]:3128", "http://host.docker.internal:3128"},
		{"http://localhost", "http://host.docker.internal"},
		{"https://localhost:8443", "https://host.docker.internal:8443"},
		{"http://proxy.internal:3128", "http://proxy.internal:3128"},
		{"http://10.0.0.5:3128", "http://10.0.0.5:3128"},
		{"not a url", "not a url"},
	}
	for _, c := range cases {
		if got := ContainerProxyURL(c.in); got != c.want {
			t.Errorf("ContainerProxyURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestRunArgv exercises the pure run-argv builder across the resource-limit,
// proxy and egress-env branches without a real engine.
func TestRunArgv(t *testing.T) {
	argv, err := RunArgv(RunOpts{
		MountDest: true, Engine: "podman", Image: "img:1", Dest: "/d", Slug: "s", HomeDir: "/d/.home",
		RT: config.Runtime{
			Proxy: "http://p", NoProxy: "x",
			Domains: []string{"a.com"}, Egress: false, // egress off → direct proxy env
		},
		Mem: "2G", CPU: "150%", Pids: 512, Interactive: true,
		Args: []string{"bash", "-l"},
	})
	if err != nil {
		t.Fatalf("RunArgv: %v", err)
	}
	s := strings.Join(argv, " ")
	for _, w := range []string{
		"run --rm", "--user", "--memory 2G", "--cpus 1.5", "--pids-limit 512", "--userns=keep-id",
		"--add-host=host.docker.internal:host-gateway",
		"--add-host=host.containers.internal:host-gateway",
		"HTTP_PROXY=http://p", "NO_PROXY=x", "SANDBOXER_ALLOW_DOMAINS=a.com",
		// The isolated agent home is bound and used as $HOME.
		"HOME=/d/.home", "/d/.home:/d/.home:rw",
		"img:1", "bash -l",
	} {
		if !strings.Contains(s, w) {
			t.Errorf("RunArgv missing %q in:\n%s", w, s)
		}
	}
}

// TestRunArgvExact pins the run argv byte-for-byte: the prefix / commonArgs /
// image split must never reorder or alter a flag (compose parses this argv, and
// sessions fingerprint the shared block).
func TestRunArgvExact(t *testing.T) {
	argv, err := RunArgv(RunOpts{
		MountDest: true, Engine: "docker", Image: "img:1", Dest: "/d", Slug: "s", HomeDir: "/h",
		RT:  config.Runtime{Proxy: "http://p", NoProxy: "x", Domains: []string{"a.com"}},
		Mem: "1G", CPU: "2", Interactive: true,
		Args:  []string{"echo", "hi"},
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("RunArgv: %v", err)
	}
	userns := strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
	want := []string{
		"run", "--rm", "-i",
		"--user", userns, "--cap-drop=ALL", "--security-opt", "no-new-privileges",
		"--workdir", "/d", "--volume", "/d:/d:rw",
		"--env", "SANDBOXER_IN_CONTAINER=1",
		"--env", "SANDBOXER_SLUG=s", "--env", "SANDBOXER_SANDBOX_DIR=/d",
		"--env", "LANG=C.UTF-8",
		"--env", "HOME=/h", "--volume", "/h:/h:rw",
		"--add-host=host.docker.internal:host-gateway",
		"--add-host=host.containers.internal:host-gateway",
		"--memory", "1G", "--cpus", "2",
		"--env", "HTTP_PROXY=http://p", "--env", "http_proxy=http://p",
		"--env", "HTTPS_PROXY=http://p", "--env", "https_proxy=http://p",
		"--env", "NO_PROXY=x", "--env", "no_proxy=x",
		"--env", "SANDBOXER_ALLOW_DOMAINS=a.com",
		"img:1",
		"echo", "hi",
	}
	if !slices.Equal(argv, want) {
		t.Errorf("RunArgv =\n%q\nwant\n%q", argv, want)
	}
}

// TestRunArgvSrcMounts: source worktrees are bind-mounted rw at their own host
// paths, and NO git plumbing ever reaches the argv — no git-dir mount, no
// GIT_CONFIG_* env (the git-outside model: the mounted directories are the
// wall, commits happen on the host).
func TestRunArgvSrcMounts(t *testing.T) {
	o := RunOpts{
		MountDest: true, Engine: "docker", Image: "img:1", Dest: "/d", Slug: "s", HomeDir: "/h",
		SrcMounts: []string{"/repos/lib", "/repos/proto"},
		Args:      []string{"true"},
	}
	argv, err := RunArgv(o)
	if err != nil {
		t.Fatalf("RunArgv: %v", err)
	}
	s := strings.Join(argv, " ")
	for _, w := range []string{
		"--volume /repos/lib:/repos/lib:rw",
		"--volume /repos/proto:/repos/proto:rw",
	} {
		if !strings.Contains(s, w) {
			t.Errorf("RunArgv missing %q in:\n%s", w, s)
		}
	}
	for _, bad := range []string{"GIT_CONFIG", ".git"} {
		if strings.Contains(s, bad) {
			t.Errorf("RunArgv leaked git plumbing (%q):\n%s", bad, s)
		}
	}
}

// TestMountGenFoldsIntoSessionHash: the mount fingerprint (RunOpts.MountGen)
// travels as an env var that folds into the session ConfigHash, so a host-side
// git checkout that recreates a mounted view directory (new inode → new
// fingerprint) flips the hash and the stale session is rebuilt against the fresh
// mount instead of reusing the orphaned one. And — the load-bearing half —
// leaving MountGen empty (a sandbox with no individual mounts) emits no flag, so
// its argv and hash are byte-identical to a pre-MountGen sandbox: no mass
// session rebuild on upgrade.
func TestMountGenFoldsIntoSessionHash(t *testing.T) {
	base := RunOpts{
		Engine: "docker", Image: "img:1", Dest: "/d", Slug: "s", HomeDir: "/h",
		SrcMounts: []string{"/d/repo/feat/x/src/proto"},
	}

	// empty MountGen → no env flag, argv unchanged
	empty, err := RunArgv(base)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(empty, " "), "SANDBOXER_MOUNT_GEN") {
		t.Error("empty MountGen still emitted SANDBOXER_MOUNT_GEN — every existing session would rebuild on upgrade")
	}

	// a set MountGen → the env flag appears
	o1 := base
	o1.MountGen = "abc123"
	argv1, err := RunArgv(o1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(argv1, " "), "--env SANDBOXER_MOUNT_GEN=abc123") {
		t.Errorf("MountGen not emitted:\n%s", strings.Join(argv1, " "))
	}

	// the session hash tracks the fingerprint: same value → same hash, changed
	// value (a recreated view dir) → different hash → rebuild.
	h1 := ConfigHash(o1, "", "")
	o1same := base
	o1same.MountGen = "abc123"
	if ConfigHash(o1same, "", "") != h1 {
		t.Error("same MountGen produced a different hash — a re-enter would spuriously rebuild")
	}
	o2 := base
	o2.MountGen = "def456" // the view dir was recreated on the host
	if ConfigHash(o2, "", "") == h1 {
		t.Error("a changed mount fingerprint did NOT flip the session hash — the stale mount would be reused")
	}
	// and an unfingerprinted sandbox hashes identically to a pre-MountGen one.
	if ConfigHash(base, "", "") == h1 {
		t.Error("empty vs set MountGen hash the same — the guard is inert")
	}
}

// TestRunArgvNarrowedNeverMountsDest pins THE containment invariant of a
// narrowed sandbox.
//
// The worktrees under Dest are COMPLETE on the host (that is the point — an IDE
// opens them), so the only thing keeping the excluded files away from the
// container is the absence of a Dest mount. A stray `--volume /d:/d` would hand
// the agent the entire repository while every other test still passed: the view
// mounts would be there, the sandbox would work, and the wall would be gone.
// This test exists so that mistake cannot ship quietly.
func TestRunArgvNarrowedNeverMountsDest(t *testing.T) {
	argv, err := RunArgv(RunOpts{
		Engine: "docker", Image: "img:1", Dest: "/d", Slug: "s", HomeDir: "/h",
		MountDest: false, // narrowed
		SrcMounts: []string{"/d/repo/feat/x/src/proto"},
		Args:      []string{"true"},
	})
	if err != nil {
		t.Fatalf("RunArgv: %v", err)
	}
	for i, a := range argv {
		if a != "--volume" || i+1 >= len(argv) {
			continue
		}
		if host, _, ok := strings.Cut(argv[i+1], ":"); ok && host == "/d" {
			t.Fatalf("narrowed sandbox mounts its root (%q) — the whole repo is exposed:\n%s",
				argv[i+1], strings.Join(argv, " "))
		}
	}
	// The view mount itself is still there — the wall must not be a broken sandbox.
	if s := strings.Join(argv, " "); !strings.Contains(s, "--volume /d/repo/feat/x/src/proto:/d/repo/feat/x/src/proto:rw") {
		t.Errorf("view mount missing:\n%s", s)
	}
	// The workdir stays the sandbox root even though it is not mounted: the
	// engine materializes it in the container layer.
	if s := strings.Join(argv, " "); !strings.Contains(s, "--workdir /d") {
		t.Errorf("workdir is not the sandbox root:\n%s", s)
	}
}

// TestEnsureImage covers the preflight: present image is a no-op; a missing
// custom image is left to the engine; a missing bundled default is auto-built
// (or fails fast with a build hint when auto-build is disabled).
func TestEnsureImage(t *testing.T) {
	// Override the test seams; restore after.
	origExists, origBuild := imageExists, buildImage
	defer func() { imageExists, buildImage = origExists, origBuild }()

	// 1. Present image → no build, no error.
	imageExists = func(string, string) bool { return true }
	built := false
	buildImage = func(toolbox.BuildOpts) error { built = true; return nil }
	if err := ensureImage(RunOpts{MountDest: true, Engine: "e", Image: config.DefaultImage}); err != nil || built {
		t.Errorf("present image: err=%v built=%v; want nil/false", err, built)
	}

	// 2. Missing CUSTOM image with an empty spec → nil (engine pulls), no build.
	imageExists = func(string, string) bool { return false }
	built = false
	if err := ensureImage(RunOpts{MountDest: true, Engine: "e", Image: "ghcr.io/x/y:1"}); err != nil || built {
		t.Errorf("custom missing image: err=%v built=%v; want nil/false", err, built)
	}

	// 3. Missing default + auto-build disabled → fail fast with the hint.
	t.Setenv("SANDBOXER_NO_AUTOBUILD", "1")
	built = false
	err := ensureImage(RunOpts{MountDest: true, Engine: "e", Image: config.DefaultImage})
	if err == nil || !strings.Contains(err.Error(), "sandboxer image build") {
		t.Errorf("autobuild-disabled: err=%v; want a 'sandboxer image build' hint", err)
	}
	if built {
		t.Error("autobuild-disabled should not build")
	}

	// 4. Missing default + auto-build → builds, then present.
	t.Setenv("SANDBOXER_NO_AUTOBUILD", "")
	calls := 0
	imageExists = func(string, string) bool { calls++; return calls > 1 } // missing then present
	gotOpts := toolbox.BuildOpts{}
	buildImage = func(o toolbox.BuildOpts) error { gotOpts = o; return nil }
	if err := ensureImage(RunOpts{MountDest: true, Engine: "podman", Image: config.DefaultImage, Stderr: &bytes.Buffer{}}); err != nil {
		t.Errorf("auto-build: unexpected err %v", err)
	}
	if gotOpts.Engine != "podman" || gotOpts.Image != config.DefaultImage {
		t.Errorf("auto-build passed wrong opts: %+v", gotOpts)
	}

	// 5. Missing VARIANT image (non-empty spec) → auto-built with the spec
	// forwarded, even though the tag is not the default image. The spec
	// carries concrete revs, as resolveImage's PinSpec guarantees at enter.
	rev := strings.Repeat("a", 40)
	spec := toolbox.Spec{Attrs: []string{"ripgrep"}, NixpkgsRev: rev, LLMAgentsRev: rev}
	variant := spec.Tag()
	calls = 0
	imageExists = func(string, string) bool { calls++; return calls > 1 }
	gotOpts = toolbox.BuildOpts{}
	if err := ensureImage(RunOpts{MountDest: true, Engine: "docker", Image: variant, Spec: spec, Stderr: &bytes.Buffer{}}); err != nil {
		t.Errorf("variant auto-build: unexpected err %v", err)
	}
	if gotOpts.Image != variant || len(gotOpts.Spec.Attrs) != 1 || gotOpts.Spec.Attrs[0] != "ripgrep" {
		t.Errorf("variant build passed wrong opts: %+v", gotOpts)
	}

	// 6. SANDBOXER_NO_AUTOBUILD also gates a missing variant.
	t.Setenv("SANDBOXER_NO_AUTOBUILD", "1")
	imageExists = func(string, string) bool { return false }
	built = false
	buildImage = func(toolbox.BuildOpts) error { built = true; return nil }
	err = ensureImage(RunOpts{MountDest: true, Engine: "e", Image: variant, Spec: spec})
	if err == nil || !strings.Contains(err.Error(), "sandboxer image build") || built {
		t.Errorf("variant autobuild-disabled: err=%v built=%v; want a build-image hint, no build", err, built)
	}
	t.Setenv("SANDBOXER_NO_AUTOBUILD", "")

	// 7. Build "succeeds" but image still absent → error.
	imageExists = func(string, string) bool { return false }
	buildImage = func(toolbox.BuildOpts) error { return nil }
	if err := ensureImage(RunOpts{MountDest: true, Engine: "e", Image: config.DefaultImage, Stderr: &bytes.Buffer{}}); err == nil {
		t.Error("still-missing after build should error")
	}
}

// TestContainerRunEgressNoDomains: egress on with an empty allowlist is a
// misconfiguration, not an open-network run.
func TestContainerRunEgressNoDomains(t *testing.T) {
	code, err := Run(RunOpts{
		MountDest: true, Engine: "true", Image: "img", Dest: t.TempDir(), Slug: "s",
		RT:       config.Runtime{Egress: true}, // no domains
		NoEgress: false, Args: []string{"true"},
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
	})
	if err == nil || code != 0 {
		t.Fatalf("egress on with no domains = (%d,%v), want (0, error)", code, err)
	}
}

// TestNestedContainerArgs pins the opt-in nested-podman relaxations. The off
// case matters as much as the on case: a profile that does not ask must produce
// argv byte-identical to before the knob existed, or every live session's
// ConfigHash moves and they all get recreated.
func TestNestedContainerArgs(t *testing.T) {
	opts := func(p *config.Profile) RunOpts {
		return RunOpts{MountDest: true, Engine: "docker", Image: "img:1", Dest: "/d", Slug: "s", Profile: p}
	}

	// A full set of existing id files, as the CLI generates them.
	writeIDFiles := func(t *testing.T) NestedIDFiles {
		t.Helper()
		dir := t.TempDir()
		ids := NestedIDFiles{
			Passwd: filepath.Join(dir, "passwd"), Group: filepath.Join(dir, "group"),
			Subuid: filepath.Join(dir, "subuid"), Subgid: filepath.Join(dir, "subgid"),
		}
		for _, p := range []string{ids.Passwd, ids.Group, ids.Subuid, ids.Subgid} {
			if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return ids
	}

	t.Run("nil profile stays off", func(t *testing.T) {
		if got := nestedContainerArgs(RunOpts{}); got != nil {
			t.Errorf("nil profile = %v, want nil", got)
		}
	})
	t.Run("profile that did not ask stays off", func(t *testing.T) {
		if got := nestedContainerArgs(opts(&config.Profile{})); got != nil {
			t.Errorf("unset = %v, want nil", got)
		}
	})
	t.Run("off keeps argv byte-identical", func(t *testing.T) {
		base, _ := RunArgv(opts(nil))
		off, _ := RunArgv(opts(&config.Profile{}))
		if !slices.Equal(base, off) {
			t.Errorf("argv moved with the knob off:\nbase = %v\n off = %v", base, off)
		}
	})
	t.Run("on adds exactly what rootless podman needs", func(t *testing.T) {
		argv, _ := RunArgv(opts(&config.Profile{NestedContainers: true}))
		s := strings.Join(argv, " ")
		for _, want := range []string{
			"--security-opt seccomp=unconfined",     // clone(CLONE_NEWUSER)
			"--security-opt systempaths=unconfined", // the nested procfs mount
			"--device /dev/net/tun",                 // pasta
			"--device /dev/fuse",                    // fuse-overlayfs
		} {
			if !strings.Contains(s, want) {
				t.Errorf("argv missing %q: %v", want, argv)
			}
		}
		// The opt-in buys a user namespace, NOT privilege: the rest of the
		// posture has to survive it.
		for _, keep := range []string{"--cap-drop=ALL", "no-new-privileges"} {
			if !strings.Contains(s, keep) {
				t.Errorf("opt-in dropped %q from the posture: %v", keep, argv)
			}
		}
		for _, never := range []string{"--privileged", "--cap-add"} {
			if strings.Contains(s, never) {
				t.Errorf("opt-in must never reach for %q: %v", never, argv)
			}
		}
	})
	t.Run("docker never gets the multi-uid grant, even with id files", func(t *testing.T) {
		o := opts(&config.Profile{NestedContainers: true})
		o.NestedIDFiles = writeIDFiles(t)
		s := strings.Join(nestedContainerArgs(o), " ")
		for _, never := range []string{"--cap-add", "/etc/passwd", "/etc/subuid"} {
			if strings.Contains(s, never) {
				t.Errorf("docker argv gained %q: %v", never, s)
			}
		}
	})
	t.Run("podman without id files stays single-uid", func(t *testing.T) {
		o := opts(&config.Profile{NestedContainers: true})
		o.Engine = "podman"
		s := strings.Join(nestedContainerArgs(o), " ")
		if strings.Contains(s, "--cap-add") || strings.Contains(s, "/etc/subuid") {
			t.Errorf("no id files but multi-uid flags emitted: %v", s)
		}
	})
	t.Run("podman with a partial id set stays single-uid", func(t *testing.T) {
		o := opts(&config.Profile{NestedContainers: true})
		o.Engine = "podman"
		o.NestedIDFiles = writeIDFiles(t)
		if err := os.Remove(o.NestedIDFiles.Subgid); err != nil {
			t.Fatal(err)
		}
		if s := strings.Join(nestedContainerArgs(o), " "); strings.Contains(s, "--cap-add") {
			t.Errorf("partial id set but multi-uid flags emitted: %v", s)
		}
	})
	t.Run("podman with id files gets exactly the multi-uid grant", func(t *testing.T) {
		o := opts(&config.Profile{NestedContainers: true})
		o.Engine = "podman"
		o.NestedIDFiles = writeIDFiles(t)
		args := nestedContainerArgs(o)
		s := strings.Join(args, " ")
		for _, want := range []string{
			"--cap-add SETUID", "--cap-add SETGID",
			"--volume " + o.NestedIDFiles.Passwd + ":/etc/passwd:ro",
			"--volume " + o.NestedIDFiles.Group + ":/etc/group:ro",
			"--volume " + o.NestedIDFiles.Subuid + ":/etc/subuid:ro",
			"--volume " + o.NestedIDFiles.Subgid + ":/etc/subgid:ro",
		} {
			if !strings.Contains(s, want) {
				t.Errorf("argv missing %q: %v", want, args)
			}
		}
		// SETUID+SETGID and NOTHING else — the grant is the two caps newuidmap
		// needs, not a general escalation; posture flags stay intact.
		if n := strings.Count(s, "--cap-add"); n != 2 {
			t.Errorf("want exactly 2 --cap-add, got %d: %v", n, args)
		}
		if strings.Contains(s, "--privileged") {
			t.Errorf("multi-uid grant must not reach for --privileged: %v", args)
		}
		argv, _ := RunArgv(o)
		full := strings.Join(argv, " ")
		for _, keep := range []string{"--cap-drop=ALL", "no-new-privileges"} {
			if !strings.Contains(full, keep) {
				t.Errorf("multi-uid grant dropped %q from the posture: %v", keep, argv)
			}
		}
	})
}

// TestContainerUser pins the --user behaviour: default = host uid:gid; the
// SANDBOXER_CONTAINER_USER escape hatch (macOS) overrides it, and an explicit
// empty value omits --user. The override must actually reach the argv.
func TestContainerUser(t *testing.T) {
	hostUser := strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())

	t.Run("default is host uid:gid", func(t *testing.T) {
		t.Setenv("SANDBOXER_CONTAINER_USER", "") // ensure clean, then unset via Unsetenv
		os.Unsetenv("SANDBOXER_CONTAINER_USER")
		got := containerUserArgs()
		if len(got) != 2 || got[0] != "--user" || got[1] != hostUser {
			t.Errorf("default = %v, want [--user %s]", got, hostUser)
		}
	})
	t.Run("override pins a value", func(t *testing.T) {
		t.Setenv("SANDBOXER_CONTAINER_USER", "0:0")
		got := containerUserArgs()
		if len(got) != 2 || got[1] != "0:0" {
			t.Errorf("override = %v, want [--user 0:0]", got)
		}
	})
	t.Run("explicit empty omits --user", func(t *testing.T) {
		t.Setenv("SANDBOXER_CONTAINER_USER", "")
		if got := containerUserArgs(); got != nil {
			t.Errorf("empty override = %v, want nil (no --user)", got)
		}
	})
	// It flows through to the real argv.
	t.Setenv("SANDBOXER_CONTAINER_USER", "1234:5678")
	argv, _ := RunArgv(RunOpts{MountDest: true, Engine: "docker", Image: "img:1", Dest: "/d", Slug: "s"})
	if !strings.Contains(strings.Join(argv, " "), "--user 1234:5678") {
		t.Errorf("override did not reach argv: %v", argv)
	}
}
