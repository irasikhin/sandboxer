package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/irasikhin/sandboxer/internal/config"
)

// newConfigInitCmd is the `init` verb of the `config` group (see
// config_cmd.go): it scaffolds a commented sandboxer.nix.
func newConfigInitCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init [name]",
		Short: "Write a starter " + config.ConfigPath(),
		Long: `Scaffold a commented ` + config.ConfigPath() + ` so you have a concrete config
to edit instead of relying on the silent defaults. It is auto-discovered by
create/enter/exec here (no -f needed). Everything lives in this one file,
including the optional inline image hook (image.hook).`,
		Example: `  sandboxer config init             # name defaults to the directory
  sandboxer config init web         # set the profile name
  sandboxer config init --force     # overwrite an existing ` + config.ConfigPath(),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := config.ConfigPath()
			if fileExists(path) && !force {
				return fmt.Errorf("%s already exists (use --force to overwrite)", path)
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
			d := config.LoadDefaults()
			if err := os.WriteFile(path, []byte(starterProfile(name, d)), 0o644); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "wrote %s (name=%s backend=%s)\n", path, name, d.Backend)
			fmt.Fprintln(out, "edit it, then: sandboxer create")
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing "+config.ConfigPath())
	return cmd
}

// maybeAutoScaffold writes a default sandboxer.nix at the project root and
// points this run at it when the user has no config at all — so create/enter
// in a fresh project land on a concrete, announced profile instead of silent
// defaults. It is a no-op (current behaviour) when an explicit -f is given, a
// project config already exists, we're inside the container, or the user opts
// out with SANDBOXER_NO_SCAFFOLD=1.
func maybeAutoScaffold(cmd *cobra.Command, f *commonFlags, pos string) error {
	if f.config != "" || inContainer() || os.Getenv("SANDBOXER_NO_SCAFFOLD") == "1" {
		return nil
	}
	root := firstNonEmpty(f.src, getwd())
	path := config.ConfigPathIn(root)
	if fileExists(path) {
		return nil // a project config already exists; leave resolution as-is
	}
	// An upgrading user with a config at a retired location/format gets a
	// clear migration hint rather than a silently-scaffolded-over default.
	legacyConfigHint(cmd.ErrOrStderr(), root)
	name := config.Sanitize(pos)
	if name == "" {
		name = config.Sanitize(filepath.Base(root))
	}
	if name == "" {
		name = "feat"
	}
	if err := os.WriteFile(path, []byte(starterProfile(name, config.LoadDefaults())), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"sandboxer: no %s — scaffolded a default (name=%s; edit it, or set SANDBOXER_NO_SCAFFOLD=1 to skip)\n", config.ConfigPath(), name)
	f.config = path
	return nil
}

// starterProfile renders a commented sandboxer.nix seeded with the effective
// defaults (so it reflects the user's environment) and the common knobs left
// as hints to fill in. The whole config — image hook included — is this one
// file.
func starterProfile(name string, d config.Defaults) string {
	domains := "\"" + strings.ReplaceAll(d.Domains, ",", "\" \"") + "\""
	return fmt.Sprintf(`# sandboxer config — a nix attrset, evaluated with a RESTRICTED nix eval (no
# network; imports/reads only inside this directory). Auto-discovered when you
# run sandboxer here (no -f needed). One profile at the top level — or several:
#   { profiles = { api = { ... }; web = { ... }; }; default = "api"; }
# Reuse between profiles is ordinary nix:
#   let base = { backend = "microsandbox"; }; in { profiles.api = base // { ... }; }
{
  # Sandbox name (slug); names the sandbox dir ./sandboxes/<name>/.
  name = %[1]q;

  # Isolation backend: microsandbox — a real VM per sandbox, on libkrun
  # (see docs/microvm.md). Container engines (docker/podman) run natively
  # INSIDE the sandbox; they are no longer host backends.
  backend = %[2]q;

  # The sources the sandbox sees — ALWAYS explicit, there is no implicit
  # default. src is a local repo path OR a git URL (https/ssh/git@/file://) —
  # a URL is cloned once into a host-side cache and worktree'd from there
  # (recreate re-fetches; review/push its branch on the host). Each entry
  # becomes a git worktree on the host, checked out on the branch YOU name —
  # branch is REQUIRED and also names the worktree's on-disk location
  # (./sandboxes/<name>/<branch>/<repo>). ONLY the selected directories are
  # visible inside the sandbox (git itself never is; review and commit on
  # the host). include lists directories: literal anchored paths
  # ("/services/api/") or patterns ("/services/*/", "**/proto/" — a whole "**"
  # segment matches any depth). srcs edits apply on the next enter/exec — even
  # a running session sees them live.
  #
  # git = "ro" | "rw" opts ONE source out of that default and shares its
  # repository's git dir, so git works inside the sandbox: "ro" for history
  # (log/diff/blame; the host repo cannot be written), "rw" to let the agent
  # commit — which hands it the whole repository, hooks and config included.
  # Mutually exclusive with include (history would carry the excluded files
  # back in). Off everywhere at runtime: SANDBOXER_NO_GIT=1.
  srcs = [
    { src = "."; branch = "feat/%[1]s"; } # this repo, whole — rename the branch your way
    # { src = "."; branch = "devops/thing"; include = [ "/services/api/" "**/proto/" ]; }
    # { src = "../legacy"; branch = "devops/thing"; git = "ro"; }  # + git history inside
    # { src = "../shared-lib"; branch = "devops/thing"; }   # another repo, whole
    # { src = "https://github.com/org/proto"; branch = "main"; } # remote → cloned
    # { src = "../proto"; branch = "feat/proto-v2"; }       # adopt a worktree you already made
  ];

  # Egress: outbound-traffic policy. allowedDomains is the ONLY domains the
  # sandbox may reach (everything else is blocked), seeded with the effective
  # defaults (SANDBOXER_DOMAINS or the built-in set — AI APIs, package + container
  # registries). Setting allowedDomains REPLACES that default set wholesale —
  # re-list EVERY domain you need, or delete the attr to keep the full defaults.
  # An empty list means what it says: allowedDomains = [ ] reaches NOTHING (a
  # fully offline machine).
  egress = {
    # enabled = true (default) enforces allowedDomains at the VM network layer.
    # enabled = false is the escape hatch: an open network — a proxy, if set,
    # is then trusted to police egress. Off entirely at runtime:
    # SANDBOXER_NO_EGRESS=1.
    # enabled = false;
    allowedDomains = [ %[3]s ];
    # proxy = "http://localhost:9999";       # ONE proxy URL; a localhost proxy
    #                                        # is reachable from the guest
    # noProxy = "localhost,127.0.0.1,.corp"; # applied alongside proxy
  };

  # Wire YOUR host agent identity into the sandbox: (1) seed its private
  # $HOME from your agent configs — ~/.claude (settings, skills, memory) +
  # ~/.claude.json, ~/.codex, ~/.gemini, opencode/crush — as a COPY
  # (never mounted, never written back; per-file merge, your in-sandbox
  # edits always win); (2) pass through the agents' auth env vars set on
  # the host (ANTHROPIC_API_KEY, CLAUDE_CODE_OAUTH_TOKEN, ...). Claude's
  # rotating OAuth file is NOT copied — for subscription auth run
  # "claude setup-token" once and export CLAUDE_CODE_OAUTH_TOKEN on the
  # host (or /login once inside — the sandbox home persists).
  # Remove (or set false) to keep sandboxes credential-free.
  hostConfigs = true;

  # Where the worktrees live (absolute, ~, or relative to the project root);
  # default: ./sandboxes inside the project, auto-added to .gitignore. Set
  # BEFORE creating the sandbox — changing it later sets worktrees aside.
  # worktreesDir = "~/sandboxes";

  # Session mode for enter/exec: "persistent" (default) | "ephemeral".
  # session = "persistent";

  # Extra host shares / env for the sandbox (non-git trees come in here).
  # extraMounts = [ { source = "/data/cache"; target = "/data/cache"; mode = "rw"; } ];
  # env = { NODE_ENV = "development"; ANTHROPIC_MODEL = "opus"; };

  # One-time setup script (bash -lc) run inside the sandbox before the agent
  # takes over — re-run only when the script changes:
  # setup = ''
  #   npm ci
  # '';

  # Language/runtime tool packs baked into a per-profile image variant
  # (node, python, go, rust, java, …):
  # tools = [ "node" "python" ];

  # Resource caps (empty = the microVM default size): memory/cpus.
  # limits = { memory = "4G"; cpus = "2"; };

  # A PREBUILT image for this profile (optional) — a pinned release of the
  # stock toolbox, or your own published image; pulled and cached on first
  # use. Mutually exclusive with tools/customization below.
  # image.ref = "ghcr.io/irasikhin/sandboxer-toolbox:v0.76.1";

  # Custom toolbox image (optional). Sandboxes then run a content-addressed
  # variant built on first use (cached after; the stock image is untouched).
  # Everything here is flat data; anything needing pkgs at build time goes
  # into a PLAIN nixpkgs overlay file (see examples/custom-image.nix).
  # image = {
  #   packages = [ "gh" "python3Packages.requests" ];   # nixpkgs attr names
  #   files."/etc/sandboxer/rc.d/10-aliases.sh" = "alias mci='mvn clean install'";
  #   env = { SANDBOX_FLAVOR = "custom"; };
  #   overlay = "./overlay.nix";                        # final: prev: { ... }
  #   # nixpkgsRev = "<40-hex>";  # pin the input; default tracks latest
  # };
}
`, name, d.Backend, domains)
}
