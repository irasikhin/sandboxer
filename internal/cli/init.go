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
#   let base = { backend = "docker"; }; in { profiles.api = base // { ... }; }
{
  # Sandbox name (slug); names the sandbox dir ../<project>-sandboxes/<name>/.
  name = %[1]q;

  # Isolation backend: docker | podman.
  backend = %[2]q;

  # The sources the sandbox sees — ALWAYS explicit, there is no implicit
  # default. Each entry becomes a git worktree on the host, checked out on
  # the branch YOU name — branch is REQUIRED and also names the worktree's
  # directory (../<project>-sandboxes/<name>/<branch>). ONLY the selected
  # files are visible inside the container (git itself never is; review and
  # commit on the host). include uses gitignore-style patterns; srcs edits
  # apply on the next enter/exec — even a running session sees them live.
  srcs = [
    { src = "."; branch = "feat/%[1]s"; } # this repo, whole — rename the branch your way
    # { src = "."; branch = "devops/thing"; include = [ "/services/api/" "*.md" ]; }
    # { src = "../shared-lib"; branch = "devops/thing"; }   # another repo, whole
    # { src = "../proto"; branch = "feat/proto-v2"; }       # adopt an existing branch/worktree
  ];

  # Egress: outbound-traffic policy. allowedDomains is the ONLY domains the
  # sandbox may reach (everything else is blocked), seeded with the effective
  # defaults (SANDBOXER_DOMAINS or the built-in set — AI APIs, package + container
  # registries). Setting allowedDomains REPLACES that default set wholesale —
  # re-list EVERY domain you need, or delete the attr to keep the full defaults.
  egress = {
    # enabled = true (default) runs sandboxer's squid allowlist sidecar and
    # enforces allowedDomains below. enabled = false is the escape hatch: NO
    # sidecar — the agent goes straight through the proxy, which is then trusted
    # to police egress (allowedDomains/routes are IGNORED). Off entirely at runtime:
    # SANDBOXER_NO_EGRESS=1.
    # enabled = false;
    allowedDomains = [ %[3]s ];
    # proxy = "http://localhost:9999";       # ONE proxy URL; localhost is
    #                                        # rewritten to the host gateway
    # noProxy = "localhost,127.0.0.1,.corp"; # enabled = false only
    # routes = [                             # per-domain upstream proxies (allowlist on)
    #   { domains = [ "api.anthropic.com" ]; proxy = "http://bypass:8080"; }
    # ];
  };

  # Session mode for enter/exec: "persistent" (default) | "ephemeral".
  # session = "persistent";

  # Extra bind mounts / env for the container (non-git trees come in here).
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

  # Resource caps (empty = uncapped): memory/cpus/pids.
  # limits = { memory = "4G"; cpus = "2"; pids = 512; };

  # Custom toolbox image (optional). Sandboxes then run a content-addressed
  # variant built on first use (cached after; the stock image is untouched).
  # Everything here is flat data; anything needing pkgs at build time goes
  # into a PLAIN nixpkgs overlay file (see examples/custom-image.nix).
  # image = {
  #   packages = [ "gh" "python3Packages.requests" ];   # nixpkgs attr names
  #   files."/etc/sandboxer/rc.d/10-aliases.sh" = "alias mci='mvn clean install'";
  #   env = { SANDBOX_FLAVOR = "custom"; };
  #   overlay = "./overlay.nix";                        # final: prev: { ... }
  #   # llmAgentsRev = "latest"; nixpkgsRev = "<full 40-hex commit>";
  # };
}
`, name, d.Backend, domains)
}
