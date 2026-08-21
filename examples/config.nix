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
  # the toolbox image (works with any agent): microsandbox.
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

  # Published ports: the sandbox's only inbound path (empty = none). Each entry
  # forwards a host port into the guest, so a dev server — or dsh's web UI —
  # opens in the host's browser. Binds 127.0.0.1 unless the spec names another
  # address; the server inside must listen on 0.0.0.0. A forward is part of the
  # machine's create argv, so adding one applies when the session is rebuilt
  # (sandboxer stop <slug> && sandboxer enter <slug>).
  ports = [
    "3080"              # 127.0.0.1:3080 on the host → 3080 in the sandbox
    # "8080:3080"       # host 8080 → guest 3080
    # "0.0.0.0:8080:3080" # every host interface (a warning says so)
    # "5353:53/udp"     # UDP
  ];
}
