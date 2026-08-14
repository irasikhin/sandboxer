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
		"gnutar", "gzip", "delta", "gnumake", "unzip",
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

// TestImageBakesNestedPodman guards the nested-podman layer end to end: the
// runtime pieces (shadow carries newuidmap/newgidmap, what a MULTI-uid nested
// namespace is built with), and the image-side bits without which a rootless
// podman inside the sandbox cannot pull anything — /var/tmp (containers/image
// stages blobs there), a storage.conf whose ignore_chown_errors absorbs the
// single-uid FALLBACK mapping (docker engine, or no host subordinate ranges),
// and a containers.conf (its one setting silences the compose provider
// banner). The launcher half is backend.nestedContainerArgs.
func TestImageBakesNestedPodman(t *testing.T) {
	s := imageDefinition(t)
	for _, want := range []string{
		"podman", "crun", "conmon", "netavark", "aardvark-dns", "passt", "fuse-overlayfs",
		"shadow",
		`writeTextDir "etc/containers/policy.json"`,
		`writeTextDir "etc/containers/storage.conf"`,
		"ignore_chown_errors",
		`writeTextDir "etc/containers/containers.conf"`,
		"compose_warning_logs = false",
		"/var/tmp",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("images.nix missing nested-podman piece %q", want)
		}
	}
}

// TestImageBakesPodmanSocket guards the testcontainers layer: the nested
// podman's docker-compatible API socket — the docker.sock testcontainers and
// docker clients connect to — is started lazily by a podman-socket helper,
// wired into BOTH entry paths (the interactive rc for enter/tmux panes, the
// CLI's exec/run wrap) and into the image env (DOCKER_HOST points clients at
// the socket; TESTCONTAINERS_RYUK_DISABLED skips the reaper sidecar that is
// podman's #1 failure mode — the disposable sandbox machine is the cleanup
// boundary instead), so a testcontainers suite works with zero configuration.
func TestImageBakesPodmanSocket(t *testing.T) {
	s := imageDefinition(t)
	for _, want := range []string{
		`writeShellScriptBin "podman-socket"`,
		"podman system service",
		"unix:///var/run/docker.sock",
		`"DOCKER_HOST=unix:///var/run/docker.sock"`,
		"TESTCONTAINERS_RYUK_DISABLED=true",
		"podmanSocket",
		"command -v podman-socket", // the rc.sh wiring
	} {
		if !strings.Contains(s, want) {
			t.Errorf("images.nix missing podman-socket piece %q", want)
		}
	}
}

// TestImageBakesDockerShim guards the docker-compatibility layer: a `docker`
// on PATH that execs podman (never a real client — no daemon socket is ever
// mounted into a sandbox), and the compose provider `docker compose` needs.
func TestImageBakesDockerShim(t *testing.T) {
	s := imageDefinition(t)
	for _, want := range []string{
		`writeShellScriptBin "docker"`,
		`exec ${pkgs.podman}/bin/podman "$@"`,
		"dockerShim",
		"podman-compose",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("images.nix missing docker-shim piece %q", want)
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
