package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/config"
)

// imageNixFileName is the starter image hook `sandboxer profile init` writes
// beside the profile under .sandboxer/; the scaffolded image.nix points at it
// by its bare relative name (see starterImageSection), resolved against
// .sandboxer/.
const imageNixFileName = "image.nix"

// imageNixPath is the cwd-relative location of the scaffolded image hook —
// .sandboxer/image.nix — beside .sandboxer/config.yaml.
func imageNixPath() string { return filepath.Join(config.StateDirName, imageNixFileName) }

// newProfileInitCmd is the `init` verb of the `profile` group (see profile.go):
// it scaffolds a commented .sandboxer/config.yaml (and the image.nix hook).
func newProfileInitCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init [name]",
		Short: "Write a starter " + config.ConfigPath() + " (and " + imageNixPath() + ")",
		Long: `Scaffold a commented ` + config.ConfigPath() + ` so you have a concrete config
to edit instead of relying on the silent defaults. It is auto-discovered by
create/enter/exec here (no -f needed). A starter ` + imageNixFileName + ` image hook is
written alongside it under ` + config.StateDirName + `/, wired in via the profile's image:
section (delete that block for the stock toolbox image).`,
		Example: `  sandboxer profile init            # name defaults to the directory
  sandboxer profile init web        # set the profile name
  sandboxer profile init --force    # overwrite an existing ` + config.ConfigPath(),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := config.ConfigPath()
			nixPath := imageNixPath()
			if fileExists(path) && !force {
				return fmt.Errorf("%s already exists (use --force to overwrite)", path)
			}
			if fileExists(nixPath) && !force {
				return fmt.Errorf("%s already exists (use --force to overwrite)", nixPath)
			}
			name := config.Sanitize(posArg(args))
			if name == "" {
				if wd, err := os.Getwd(); err == nil {
					name = config.Sanitize(filepath.Base(wd))
				}
			}
			if name == "" {
				name = "feat"
			}
			if err := os.MkdirAll(config.StateDirName, 0o755); err != nil {
				return err
			}
			d := config.LoadDefaults()
			if err := os.WriteFile(path, []byte(starterProfile(name, d)), 0o644); err != nil {
				return err
			}
			if err := os.WriteFile(nixPath, []byte(starterImageNix), 0o644); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "wrote %s + %s (name=%s backend=%s agent=%s)\n", path, nixPath, name, d.Backend, d.Agent)
			fmt.Fprintln(out, "edit them, then: sandboxer create")
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing "+config.ConfigPath()+"/"+imageNixPath())
	return cmd
}

// maybeAutoScaffold writes a default .sandboxer/config.yaml into the project's
// state dir and points this run at it when the user has no config at all — so
// create/enter in a fresh project land on a concrete, announced profile instead
// of silent defaults. It is a no-op (current behaviour) when an explicit -f is
// given, a project config already exists, we're inside the container, or the
// user opts out with SANDBOXER_NO_SCAFFOLD=1.
//
// Like the explicit `init`, it scaffolds the active image: section and an
// image.nix hook beside the config under .sandboxer/, so the custom image works
// on the auto-scaffold path too (enter/exec/run build the variant on first use,
// with a one-time notice). An existing image.nix is left untouched.
func maybeAutoScaffold(cmd *cobra.Command, f *commonFlags, pos string) error {
	if f.config != "" || inContainer() || os.Getenv("SANDBOXER_NO_SCAFFOLD") == "1" {
		return nil
	}
	root := firstNonEmpty(f.src, getwd())
	path := filepath.Join(root, config.StateDirName, config.ConfigFileName)
	if fileExists(path) {
		return nil // a project config already exists; leave resolution as-is
	}
	// An upgrading user with a stale root-level .sandboxer.yaml gets a clear
	// migration hint rather than a silently-scaffolded-over default.
	legacyConfigHint(cmd.ErrOrStderr(), root)
	name := config.Sanitize(pos)
	if name == "" {
		name = config.Sanitize(filepath.Base(root))
	}
	if name == "" {
		name = "feat"
	}
	if err := os.MkdirAll(filepath.Join(root, config.StateDirName), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(starterProfile(name, config.LoadDefaults())), 0o644); err != nil {
		return err
	}
	nixPath := filepath.Join(root, config.StateDirName, imageNixFileName)
	if !fileExists(nixPath) {
		if err := os.WriteFile(nixPath, []byte(starterImageNix), 0o644); err != nil {
			return err
		}
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"sandboxer: no %s — scaffolded a default (name=%s; edit it, or set SANDBOXER_NO_SCAFFOLD=1 to skip)\n", config.ConfigPath(), name)
	f.config = path
	return nil
}

// starterProfile renders a commented .sandboxer/config.yaml seeded with the
// effective defaults (so it reflects the user's environment) and the common
// knobs left as hints to fill in, plus an active image: section wired to the
// image.nix hook both init and auto-scaffold write alongside under .sandboxer/.
func starterProfile(name string, d config.Defaults) string {
	domains := strings.ReplaceAll(d.Domains, ",", ", ")
	profile := fmt.Sprintf(`# yaml-language-server: $schema=`+config.SchemaURL+`
# sandboxer profile — edit to taste. Auto-discovered when you run sandboxer
# in this directory (no -f needed). Full reference: examples/ in the repo.

# Sandbox name (slug); drives .sandboxer/<name>/.
name: %s

# Isolation backend: docker | podman.
backend: %s

# Coding agent — see: sandboxer agents.
agent: %s

# Session mode for enter/exec: persistent (default; one detached container
# reused across invocations) | ephemeral (a fresh one-shot container each time).
# session: persistent

# Egress allowlist: the ONLY domains the sandbox may reach (everything else is
# blocked). Seeded with the effective defaults (SANDBOXER_DOMAINS or the built-in
# set — AI APIs + the common package registries: npm, PyPI, Maven, Gradle,
# crates, Go, RubyGems, GitHub). Setting allowedDomains REPLACES that default set
# wholesale — re-list EVERY domain you need (not just additions), or delete this
# block to keep the full defaults. Trim to what your task needs.
network:
  allowedDomains: [%s]

# Sandbox content. Nothing is copied unless listed here: each dep is located by
# path suffix under roots — this directory is always searched as an implicit
# last root — copied INTO the sandbox's workspace/, and pushed back with
# 'sandboxer push'. Uncomment and adjust:
# deps:
#   - src/lib
# roots: only needed to search OTHER trees besides this directory:
# roots:
#   - ~/work/other-repo

# Agent context: project files copied to the sandbox ROOT (beside workspace/)
# so agents see your instructions — refreshed on pull, never pushed back.
# Default when unset: CLAUDE.md, AGENTS.md, .claude (existing entries only).
# Listing context: REPLACES that set, so re-list what you keep:
# context: [CLAUDE.md, AGENTS.md, .claude, docs/agent-notes.md]

# Extra bind mounts / env for the container backend (optional). To pin the
# agent's model, set the agent's own env var here (e.g. ANTHROPIC_MODEL for
# claude) — sandboxer has no model: knob, it never launches the agent itself.
# extraMounts:
#   - { source: /data/cache, target: /data/cache, mode: rw }
# env:
#   NODE_ENV: development
#   ANTHROPIC_MODEL: opus

# One-time setup script (bash -lc) run inside the sandbox before the agent takes
# over — re-run only when the script changes:
# setup: |
#   npm ci

# Language/runtime tool packs baked into a per-profile image variant
# (see registry/tools.json: node, python, go, rust, …):
# tools: [node, python]

# Resource caps for the sandbox container (empty = uncapped). memory/cpus
# override SANDBOXER_MEM/SANDBOXER_CPU; pids is a --pids-limit (fork-bomb guard).
# limits:
#   memory: 4G
#   cpus: 2
#   pids: 512

# MCP servers wired into the agent (see registry/mcp.json); their domains are
# folded into the egress allowlist automatically:
# mcp: [context7]

# Turn the egress allowlist off entirely for this profile (default: on):
# egress: false

# Proxy — ONE URL; the egress toggle (above) decides the mode:
#   egress on  (default): allowlist stays on, traffic is CHAINED through the
#               proxy (allowedDomains still enforced). http:// only.
#   egress off: the agent talks to the proxy DIRECTLY; the proxy polices egress.
#               http:// or https://; noProxy applies.
# A localhost/127.0.0.1 proxy means a proxy on your HOST (rewritten to the host
# gateway automatically). Global default: agentProxy: / SANDBOXER_PROXY.
# proxy: http://localhost:9999
# noProxy: localhost,127.0.0.1,.corp   # direct mode only (egress off)
`, name, d.Backend, d.Agent, domains)
	profile += starterImageSection
	return profile
}

// starterImageSection is the active image: block appended by `sandboxer init`.
// It points at the image.nix hook written alongside under .sandboxer/ by its
// bare relative name (resolved against the profile's directory); the spec is
// non-empty (a nix hook is set), so the first create builds a content-addressed
// toolbox variant, cached thereafter — the stock image is untouched.
const starterImageSection = `
# Custom toolbox image (optional). Sandboxes in this profile run a
# content-addressed variant built on first 'create' (cached after; the stock
# sandboxer-toolbox:latest is untouched). Delete this block for the stock image.
image:
  extraPkgs: []            # extra nixpkgs attrs baked in (dotted paths allowed)
  nix: ` + imageNixFileName + `   # { packages, files, env, overlay } hook — see that file
  # llmAgentsRev: latest   # flake-input pin overrides; empty keeps embedded pins
  # nixpkgsRev: <full 40-char hex commit>
`

// starterImageNix is the annotated, inert image hook `sandboxer init` writes.
// Every example is commented, so it evaluates to { } (the variant is content-
// equivalent to the stock image) until the user uncomments something.
const starterImageNix = `# image.nix — the image hook this profile's image: section points at,
# imported by the embedded toolbox flake during 'sandboxer image build' (or the
# auto-build on first enter). A function over { pkgs } returning any of FOUR
# keys: packages, files, env, overlay. The contract is fail-closed — an unknown
# key aborts the build, so a typo never silently drops a customization.
#
# Two-phase evaluation: the function is first called with BASE nixpkgs and only
# 'overlay' is read; nixpkgs is then re-imported with the overlay applied, and
# the function is called again with that final 'pkgs' for packages/files/env.
# Everything below is commented — uncomment what you need.
{ pkgs }:
{
  # nixpkgs overlay (phase 1): add or override packages; everything in phase 2
  # sees the result. (An overlay cannot reference its own result.)
  # overlay = final: prev: {
  #   hello-sandboxer = prev.writeShellScriptBin "hello-sandboxer" ''
  #     echo "hello from a custom toolbox image"
  #   '';
  # };

  # Extra store paths baked into the image — 'pkgs' has the overlay applied.
  # packages = [
  #   pkgs.httpie
  # ];

  # Text files at absolute paths inside the image. /etc/sandboxer/rc.d/*.sh are
  # the shell's plugin drop-ins, sourced by every interactive shell.
  # files = {
  #   "/etc/sandboxer/rc.d/10-custom.sh" = ''
  #     alias hi=hello-sandboxer
  #   '';
  # };

  # Appended to the image's OCI env; a user variable overrides a same-named
  # default (last occurrence wins).
  # env = {
  #   SANDBOX_FLAVOR = "custom";
  # };
}
`
