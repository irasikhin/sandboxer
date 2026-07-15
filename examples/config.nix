# Minimal sandbox config.
#
# sandboxer auto-discovers `sandboxer.nix` in the current directory (or pass
# one explicitly: `sandboxer create --config path/feat.nix`, or positionally
# `sandboxer create feat.nix`). This file is a standalone sample of the flat
# single-profile form — copy it to ./sandboxer.nix to use it.
{
  # Sandbox name (slug). Drives the worktree branch feat/<name>-sb.
  name = "feature-x";

  # Isolation backend — a podman or docker container built from the toolbox
  # image (works with any agent).
  backend = "podman";

  # The sources the sandbox sees — always explicit ({ src = "."; } = this
  # repo, whole; relative paths resolve against the project root). See
  # with-srcs.nix for include patterns, extra repos and branch adoption.
  srcs = [
    { src = "."; }
  ];

  # Egress allowlist: the ONLY domains the sandbox may reach. Setting it
  # REPLACES the built-in default set wholesale; delete the attr to keep the
  # defaults (AI APIs + common package registries).
  egress = {
    allowedDomains = [ "api.anthropic.com" "registry.npmjs.org" ];
  };
}
