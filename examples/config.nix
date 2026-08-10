# Minimal sandbox config.
#
# sandboxer auto-discovers `sandboxer.nix` in the current directory (or pass
# one explicitly: `sandboxer create --config path/feat.nix`, or positionally
# `sandboxer create feat.nix`). This file is a standalone sample of the flat
# single-profile form — copy it to ./sandboxer.nix to use it.
{
  # Sandbox name (slug). Names the sandbox dir ./sandboxes/<name>/.
  name = "feature-x";

  # Isolation backend — a real microVM per sandbox (libkrun), booted from
  # the toolbox image (works with any agent): microsandbox | microvm.
  backend = "microsandbox";

  # The sources the sandbox sees — always explicit (src = "." is this repo,
  # whole; relative paths resolve against the project root). branch is
  # REQUIRED: it names the worktree's branch and its on-disk directory
  # (./sandboxes/<name>/<branch>/<repo>, auto-git-ignored; relocate the root
  # with worktreesDir). See with-srcs.nix for include patterns, extra repos
  # and branch adoption.
  srcs = [
    { src = "."; branch = "devops/feature-x"; }
  ];

  # Egress allowlist: the ONLY domains the sandbox may reach. Setting it
  # REPLACES the built-in default set wholesale; delete the attr to keep the
  # defaults (AI APIs + common package registries).
  egress = {
    allowedDomains = [ "api.anthropic.com" "registry.npmjs.org" ];
  };
}
