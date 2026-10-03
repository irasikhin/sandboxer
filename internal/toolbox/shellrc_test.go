package toolbox

import (
	"regexp"
	"strings"
	"testing"
)

// imageDefinition returns assets/images.nix — the shared definition of what is
// IN the images, imported by BOTH the embedded flake and the repo's root flake.
// The content guards below read it rather than flake.nix, which now only
// resolves a profile's context.
func imageDefinition(t *testing.T) string {
	t.Helper()
	data, err := assets.ReadFile("assets/images.nix")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestFlakeImportsImages guards the seam itself: the embedded flake must import
// the shared image definition, and it must be rendered into the build context
// (see writeContext). Without this the content guards below could all pass
// while the flake builds nothing.
func TestFlakeImportsImages(t *testing.T) {
	data, err := assets.ReadFile("assets/flake.nix")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "import ./images.nix") {
		t.Error("embedded flake.nix must import ./images.nix — the shared image definition is not wired")
	}
}

// TestFlakeEmbedsOomWatchdog guards that the toolbox image still bakes the
// OOM watchdog: a process the guest kernel OOM-killed shows up as a bare
// "Killed" with no diagnostics, and the watchdog (first shell after the
// incident, reading /dev/kmsg) is what turns that into an actionable
// warning naming the machine's memory cap.
func TestFlakeEmbedsOomWatchdog(t *testing.T) {
	s := imageDefinition(t)
	for _, want := range []string{
		"/tmp/sandboxer-oom-seen",
		"/dev/kmsg",
		"oom-killer",
		"limits.memory / SANDBOXER_MEM",
		"MemTotal",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("images.nix missing %q — OOM watchdog not wired", want)
		}
	}
}

// TestFlakeEmbedsShellRc guards that the toolbox image still bakes the
// interactive-shell rc (the sandbox-aware prompt and the plugin/user drop-in
// hooks), so a refactor cannot silently drop the terminal UX or the `enter`
// launcher's `/etc/sandboxer/rc.sh` target.
func TestFlakeEmbedsShellRc(t *testing.T) {
	s := imageDefinition(t)
	for _, want := range []string{
		`writeTextDir "etc/sandboxer/rc.sh"`,
		"shellRc",
		"SANDBOXER_SLUG",
		"/etc/sandboxer/rc.d",
		".config/sandboxer/rc",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("images.nix missing %q — shell rc not wired", want)
		}
	}
}

// TestFlakeEmbedsGitGuard guards the in-guest git wrapper: a managed source's
// .git is a pointer file whose gitdir names an unmounted host path, and plain
// git's "fatal: not a git repository" invited an agent to "repair" the tree
// with `git init` (the live incident). The wrapper must stay wired — replacing
// bin/git, explaining the design, and refusing — and plain `git` must stay OUT
// of the base contents (a second bin/git would race the wrapper for the path).
func TestFlakeEmbedsGitGuard(t *testing.T) {
	s := imageDefinition(t)
	for _, want := range []string{
		"gitGuarded",
		"gitdir: ",
		"managed git WORKTREE",
		"do NOT 'git init' here",
		// The way OUT: the refusal must name the key that turns git on, or the
		// message is a dead end for anyone whose source legitimately needs it.
		`git = \"ro\"`,
		`git = \"rw\"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("images.nix missing %q — the git guard is not wired", want)
		}
	}
	if regexp.MustCompile(`(?m)^\s{8}git$`).MatchString(s) {
		t.Error("plain `git` is back in the image contents — it would shadow the guarded wrapper")
	}
}

// TestFlakeShipsDetachEscapeHatches guards the ways OUT of an attached
// session when the prefix key never reaches tmux — Ctrl-Space is a common
// input-method toggle, and `exit` ends the session rather than leaving it
// running, so a single prefix is a trap. C-b stays as the second prefix, Alt-d
// detaches with no prefix at all, and `detach` is a command on PATH.
func TestFlakeShipsDetachEscapeHatches(t *testing.T) {
	s := imageDefinition(t)
	for _, want := range []string{
		"set -g prefix2 C-b",
		"bind -n M-d detach-client",
		`writeShellScriptBin "detach"`,
		"detachCmd",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("images.nix missing detach escape hatch %q", want)
		}
	}
	if strings.Contains(s, "unbind C-b") {
		t.Error("C-b is unbound again — it is the fallback prefix when Ctrl-Space is eaten")
	}
}

// TestFlakeBakesToolingPack guards that the baseline tooling humans and agents
// rely on (pager, editor, process tools, search, archives, delta git pager,
// the comparison pack) stays baked into the image, and that /etc/gitconfig
// routes the pager through delta.
//
// diffutils is the load-bearing one: `diff` does not come with coreutils, and
// delta only colors git's own diffs — which a sandbox has no git for unless a
// source opted in. Dropping it puts "command not found" behind the most
// reflexive comparison command there is.
func TestFlakeBakesToolingPack(t *testing.T) {
	s := imageDefinition(t)
	for _, want := range []string{
		"less", "neovim", "procps", "ripgrep", "fd", "tree",
		"gnutar", "gzip", "delta", "jujutsu", "gnumake", "unzip",
		"diffutils", "patch", "difftastic", "dyff",
		`writeTextDir "etc/gitconfig"`, "gitConfig",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("images.nix missing tooling %q", want)
		}
	}
}

// TestFlakeBakesAgentBatteries guards the LLM-agent tooling: the everyday
// tools an agent reaches for and used to hit "command not found" on —
// network/egress forensics (dig/ip/ping/nc), YAML editing (yq), artifact
// inspection (file/binutils/xxd/zip), sponge, shellcheck, lsof, openssl and
// the GitHub CLI. The short names (file/zip/gh) are anchored to a whole
// contents line — a substring check would false-match files.json, unzip and
// ghcr.io.
func TestFlakeBakesAgentBatteries(t *testing.T) {
	s := imageDefinition(t)
	for _, want := range []string{
		"bind.dnsutils", "iproute2", "iputils", "netcat-openbsd",
		"yq-go", "binutils", "xxd", "moreutils", "shellcheck",
		"lsof", "openssl",
		// find/xargs: not in coreutils, and not replaced by fd/rg — the
		// reflexive file walk must not answer "command not found".
		"findutils",
		// the POSIX/base-system userland every script assumes: column/rev/
		// flock/script/uuidgen (util-linux), pstree/killall/fuser (psmisc),
		// the classic net trio (nettools), the archive formats tar meets
		// (bzip2/xz/zstd/cpio/p7zip), and the everyday rest.
		"util-linux", "psmisc", "nettools", "bzip2", "xz", "zstd", "cpio",
		"p7zip", "wget", "bc", "gettext", "socat", "sqlite", "strace",
		"man-db", "man-pages", "tzdata", "traceroute",
		// `java` on PATH: the JDK's own bin is a symlink the image merge
		// resolves away, so the per-tool symlinks are what PATH sees.
		"jdkBin",
		// the hyphenated compose spelling now comes from the real docker-compose
		// package (guarded in TestImageBakesDockerEngine), and a pip that says
		// use uv.
		"pipHint",
		// vi/vim, not just nvim.
		"viAlias",
		// the two env vars that make baked data findable: man pages live at
		// /share/man and the tzdb at /share/zoneinfo, neither of which any
		// default search path mentions.
		"MANPATH=/share/man", "TZDIR=/share/zoneinfo",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("images.nix missing agent tooling %q", want)
		}
	}
	for _, re := range []string{`(?m)^\s{8}file$`, `(?m)^\s{8}zip$`, `(?m)^\s{8}gh$`} {
		if !regexp.MustCompile(re).MatchString(s) {
			t.Errorf("images.nix missing agent tooling %q", re)
		}
	}
}

// TestFlakeBakesSourcePack guards the source-code tooling an agent works a
// tree with: structural search/rewrite (ast-grep), the symbol index neovim
// jumps through (universal-ctags), codebase orientation (tokei), syntax-colored
// reading with line numbers (bat), candidate filtering (fzf), the file-watch
// test loop (entr) and zero-config python lint/format (ruff). The short names
// are anchored to a whole contents line: "bat" would otherwise match
// bashInteractive and "entr" any Entrypoint.
func TestFlakeBakesSourcePack(t *testing.T) {
	s := imageDefinition(t)
	for _, want := range []string{"ast-grep", "universal-ctags", "tokei", "ruff"} {
		if !strings.Contains(s, want) {
			t.Errorf("images.nix missing source tooling %q", want)
		}
	}
	for _, re := range []string{`(?m)^\s{8}bat$`, `(?m)^\s{8}fzf$`, `(?m)^\s{8}entr$`} {
		if !regexp.MustCompile(re).MatchString(s) {
			t.Errorf("images.nix missing source tooling %q", re)
		}
	}
}

// TestImageBakesDockerEngine guards the container engine the sandbox ships:
// the REAL Docker — the client plus moby's dockerd, whose nixpkgs wrapper
// carries its own libexec/docker (containerd, runc, the shim, docker-proxy,
// docker-init), so the daemon is self-sufficient in the guest. The engine is
// the guest's own, running on the microVM kernel; no host socket is ever
// mounted. podman is GONE — the migration removed it, and a stray package or
// shim would silently hand an agent a second, incompatible engine.
func TestImageBakesDockerEngine(t *testing.T) {
	s := imageDefinition(t)
	for _, want := range []string{
		"docker", "moby", "docker-compose", "docker-buildx", "iptables", "nftables",
		// The plugins are exposed in the system dir the image points the
		// client's DOCKER_CLI_PLUGIN_DIRS at (nixpkgs patches the client so the
		// wrapper's env is the ONLY system search path), so `docker compose`
		// and `docker buildx` resolve however the client is reached.
		"dockerPlugins",
		"/usr/libexec/docker/cli-plugins",
		`"DOCKER_CLI_PLUGIN_DIRS=/usr/libexec/docker/cli-plugins"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("images.nix missing docker-engine piece %q", want)
		}
	}
	// podman and its runtime pieces are gone: a package line, a shim/helper
	// or a containers/* config file would all be a leftover of the old engine.
	for _, re := range []string{
		`(?m)^\s{8}podman$`, `(?m)^\s{8}crun$`, `(?m)^\s{8}fuse-overlayfs$`,
		`(?m)^\s{8}podman-compose$`, `(?m)^\s{8}shadow$`,
	} {
		if regexp.MustCompile(re).MatchString(s) {
			t.Errorf("images.nix still ships the podman-era package %q", re)
		}
	}
	for _, gone := range []string{"podman-socket", "dockerShim", "composeShim", "etc/containers/"} {
		if strings.Contains(s, gone) {
			t.Errorf("images.nix still references the podman-era %q", gone)
		}
	}
}

// TestImageBakesDockerDaemon guards the testcontainers layer: the guest's
// Docker daemon — the docker.sock testcontainers and docker clients connect
// to — is started by a docker-daemon helper, wired into BOTH entry paths (the
// interactive rc for enter/tmux panes, the CLI's exec/run wrap and machine
// boot). The helper is idempotent, detached, pid-guarded against a stale pid
// from a previous boot, and it waits for the daemon to actually SERVE (not
// just for the socket file), naming the log on failure. It also refuses a
// machine whose /var/lib/docker fell back to the guest's overlayfs root — a
// sandbox created without the data volume — with a hint to recreate it,
// instead of letting the user meet the opaque overlay-mount error later.
func TestImageBakesDockerDaemon(t *testing.T) {
	s := imageDefinition(t)
	for _, want := range []string{
		`writeShellScriptBin "docker-daemon"`,
		"nohup dockerd ",
		"/var/run/sandboxer-dockerd.pid",
		"/var/log/sandboxer/dockerd.log",
		`"DOCKER_HOST=unix:///var/run/docker.sock"`,
		"TESTCONTAINERS_RYUK_DISABLED=true",
		"dockerDaemon",
		"command -v docker-daemon", // the rc.sh wiring
		"overlayfs",                // the no-data-volume guard
		"dockerDisk",               // …and the knob it points the user at
	} {
		if !strings.Contains(s, want) {
			t.Errorf("images.nix missing docker-daemon piece %q", want)
		}
	}
}

// TestImageBakesLocalKubernetes guards the local-cluster toolchain. kind's
// DEFAULT provider is Docker, so the podman provider pin is gone; the node
// containers run on the guest's own dockerd, whose storage is the ext4-backed
// /var/lib/docker rather than the guest's overlayfs root — which is why the
// snapshotter question is re-measured on Docker instead of inheriting the
// podman-era fuse-overlayfs pin. kind bind-mounts /lib/modules read-only into
// every node; the empty dir is created in fakeRootCommands as belt-and-braces.
// k3d rides the same docker socket (DOCKER_HOST, brought up by docker-daemon).
// The clients are what an agent drives the cluster with — kubectl and its plugin
// family, helm plus the helm workflow (helmfile/vals) and the GitOps CLIs
// (argocd/flux/kubeseal) — and the validators answer questions about a manifest
// or a cluster with no client at all.
func TestImageBakesLocalKubernetes(t *testing.T) {
	s := imageDefinition(t)
	for _, want := range []string{
		"kubectl", "kubernetes-helm", "kustomize", "kubeconform", "stern",
		"helmfile", "vals", "kubecolor", "kubeseal", "kube-linter", "popeye", "pluto",
		"argocd", "fluxcd",
		"/lib/modules",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("images.nix missing local-kubernetes piece %q", want)
		}
	}
	// Whole-line entries: a bare substring would pass on "kubectl" alone, which
	// the pack already carries.
	for _, re := range []string{
		`(?m)^\s{8}kind$`, `(?m)^\s{8}k3d$`, `(?m)^\s{8}k9s$`, `(?m)^\s{8}kubectx$`,
		`(?m)^\s{8}kubectl-tree$`, `(?m)^\s{8}rakkess$`, `(?m)^\s{8}kubectl-ktop$`,
		`(?m)^\s{8}kubectl-view-secret$`, `(?m)^\s{8}kubectl-images$`,
	} {
		if !regexp.MustCompile(re).MatchString(s) {
			t.Errorf("images.nix missing local-kubernetes package %q", re)
		}
	}
}

// TestImageBakesHelmPlugins guards the baked helm-diff plugin: helm has no
// /etc-level plugin directory, so the image can only EXPOSE the plugin at the
// stable path /etc/sandboxer/helm-plugins/ and the CLI links it into the sandbox
// home (sandbox.EnsureHelmPlugins). Without the symlink helmfile's diff step
// would need a network-reaching `helm plugin install` inside every sandbox.
func TestImageBakesHelmPlugins(t *testing.T) {
	s := imageDefinition(t)
	for _, want := range []string{
		"/etc/sandboxer/helm-plugins",
		"kubernetes-helmPlugins.helm-diff",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("images.nix missing baked helm plugin piece %q", want)
		}
	}
}

// TestImageDropsPodmanEraKindPins guards the negative half of the k8s wiring:
// the old image pointed kind at podman's provider AND forced the fuse-overlayfs
// snapshotter, and wrapped k3d to pin --snapshotter=native, because the guest's
// only engine was podman on the overlayfs root. Docker's data-root is the ext4
// volume instead, so none of those pins may come back by accident.
func TestImageDropsPodmanEraKindPins(t *testing.T) {
	s := imageDefinition(t)
	for _, gone := range []string{
		"KIND_EXPERIMENTAL_PROVIDER",
		"k3dShim",
		"--snapshotter=native",
	} {
		if strings.Contains(s, gone) {
			t.Errorf("images.nix still carries the podman-era kind/k3d wiring %q", gone)
		}
	}
}

// TestImageBakesPythonBatteries guards that the base python3 carries the glue
// libraries baked into the image (click CLIs, YAML/TOML config, templating,
// HTTP, HTML, schema validation, a test runner), via python3.withPackages — a
// plain python3 would import-error on them — plus uv, the escape hatch for
// everything not baked: the interpreter lives in the read-only nix store, so
// `pip install` cannot work and a venv is the only way in.
func TestImageBakesPythonBatteries(t *testing.T) {
	s := imageDefinition(t)
	if !strings.Contains(s, "python3.withPackages") {
		t.Error("images.nix ships a bare python3 — the batteries (click/pyyaml/jinja2) are not wired")
	}
	for _, want := range []string{
		"click", "pyyaml", "jinja2", "requests",
		"pytest", "rich", "httpx", "tomlkit", "ruamel-yaml",
		"jsonschema", "beautifulsoup4", "lxml", "python-dateutil",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("images.nix python batteries missing %q", want)
		}
	}
	if !regexp.MustCompile(`(?m)^\s{8}uv$`).MatchString(s) {
		t.Error("images.nix missing uv — nothing outside the baked set could be installed in a sandbox")
	}
}

// TestImageBakesPkl guards the pkl CLI. nixpkgs' pkl is a JVM build whose
// runtime JRE (temurin-bin-21) alone is ~639 MiB — a third of the image — while
// the image already bakes jdk25 for maven. So the jar must be copied out of the
// pkl package and wrapped against the baked JDK, with both JVM flags that
// silence Java 25's JNA / sun.misc.Unsafe warnings on every invocation.
func TestImageBakesPkl(t *testing.T) {
	s := imageDefinition(t)
	for _, want := range []string{
		"pklCli",
		"/opt/pkl/jpkl.jar",
		"pkgs.jdk25",
		"--enable-native-access=ALL-UNNAMED",
		"--sun-misc-unsafe-memory-access=allow",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("images.nix missing pkl piece %q", want)
		}
	}
}
