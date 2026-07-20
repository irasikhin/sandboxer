# The toolbox + egress-proxy image definitions — the SINGLE source of truth for
# what is in the images, imported by both flakes that build them:
#
#   - assets/flake.nix — embedded in the sandboxer binary and built inside an
#     ephemeral `nixos/nix` container by `sandboxer image build`. This is the
#     image users actually run; it passes the profile's generated context
#     (agents/tools/overlay/files/env).
#   - the repo's root flake.nix — `nix build .#image`, used by CI and the
#     container e2e suite. It passes only the agent set; there is no profile.
#
# They were separate copies until they drifted: every image improvement landed
# in the embedded flake, so `.#image` quietly lost podman, the language runtimes
# and the shell rc, and the e2e suite tested an image no user ever gets. Keep
# the definition HERE — a flake should only supply pkgs and the package lists.
#
# The two callers still differ in one honest way: the embedded flake pins its
# nixpkgs by rev, the root flake follows its own flake input. Same contents,
# possibly different nixpkgs — that is inherent to a dev/CI convenience build.
{
  pkgs,
  # Coding agents from llm-agents (both callers derive these from the same
  # registry.json, one via a generated agents.nix, one by reading the JSON).
  agentPkgs ? [ ],
  # A profile's `tools:` packs + image.packages, already resolved to packages.
  toolPkgs ? [ ],
  # A profile's image.files, already rendered to writeTextDir derivations.
  userFiles ? [ ],
  # A profile's image.env as "K=V" strings, appended after the defaults.
  userEnv ? [ ],
}:
let
  # Interactive-shell rc baked into the image: a sandbox-aware colored
  # prompt, sane aliases + EDITOR/PAGER, and drop-in extension points
  # (/etc/sandboxer/rc.d/*.sh for plugins, ~/.config/sandboxer/rc for
  # the user). `enter` launches `bash --rcfile /etc/sandboxer/rc.sh -i`.
  # Kept in the image — never seeded into the per-sandbox $HOME — so
  # shell cosmetics never touch the agent's private home.
  shellRc = pkgs.writeTextDir "etc/sandboxer/rc.sh" ''
    # sourced via `bash --rcfile`; do nothing for non-interactive shells
    case $- in *i*) ;; *) return ;; esac

    __sbx_git() {
      local b
      b=$(git rev-parse --abbrev-ref HEAD 2>/dev/null) || return
      printf ' (%s)' "$b"
    }

    # The magenta sbx:<slug> marker means this is never mistaken for the
    # host shell; cwd in cyan; git branch when inside a repo.
    PS1='\[\e[1;35m\]sbx:'"''${SANDBOXER_SLUG:-?}"'\[\e[0m\] \[\e[36m\]\w\[\e[0m\]$(__sbx_git)\$ '

    alias ls='ls --color=auto'
    alias ll='ls -alh --color=auto'
    alias la='ls -A'
    alias grep='grep --color=auto'
    alias ..='cd ..'

    export EDITOR="''${EDITOR:-nvim}"
    export PAGER="''${PAGER:-less}"
    export LESS="''${LESS:--R}"

    # extension points: plugin drop-ins (image) then the user file (home)
    for f in /etc/sandboxer/rc.d/*.sh; do [ -r "$f" ] && . "$f"; done
    [ -r "$HOME/.config/sandboxer/rc" ] && . "$HOME/.config/sandboxer/rc"
  '';

  # System git config: route the pager through delta for readable diffs.
  # Safe for headless agents — git disables the pager when stdout is not
  # a TTY, so porcelain/parseable output is untouched; this only affects
  # the human-facing interactive pager.
  gitConfig = pkgs.writeTextDir "etc/gitconfig" ''
    [core]
        pager = delta
    [interactive]
        diffFilter = delta --color-only
    [delta]
        navigate = true
  '';

  # Nested-podman plumbing: podman refuses to run without a signature
  # policy, and pulls need a registry search list. Mirrors are a
  # per-user concern: add them in the sandbox at
  # ~/.config/containers/registries.conf (rootless podman lets the
  # $HOME config override this system one, and the sandbox $HOME
  # persists).
  containersPolicy = pkgs.writeTextDir "etc/containers/policy.json" ''
    { "default": [ { "type": "insecureAcceptAnything" } ] }
  '';

  # Storage for the nested podman. The sandbox user has no subordinate
  # uid range (no /etc/subuid, and no setuid newuidmap could work under
  # --security-opt no-new-privileges anyway), so podman falls back to a
  # SINGLE-uid namespace. Unpacking a normal image then fails, because
  # its files are owned by ids that do not exist in that namespace
  # (alpine's /etc/shadow is 0:42) — `ignore_chown_errors` is exactly
  # the escape hatch for that case: the chown is skipped instead of
  # aborting the pull. fuse-overlayfs (not vfs) keeps layers shared;
  # it needs /dev/fuse, which the launcher passes only for a profile
  # that opted in (see backend.nestedContainerArgs).
  containersStorage = pkgs.writeTextDir "etc/containers/storage.conf" ''
    [storage]
    driver = "overlay"
    [storage.options]
    mount_program = "${pkgs.fuse-overlayfs}/bin/fuse-overlayfs"
    ignore_chown_errors = "true"
  '';
  containersRegistries = pkgs.writeTextDir "etc/containers/registries.conf" ''
    unqualified-search-registries = ["docker.io"]
    # Mirrors (e.g. when docker.io is throttled where you are): copy
    # this file to ~/.config/containers/registries.conf inside the
    # sandbox and add
    #   [[registry]]
    #   prefix = "docker.io"
    #   location = "registry-1.docker.io"
    #   [[registry.mirror]]
    #   location = "mirror.gcr.io"
  '';

  # System tmux config at /etc/tmux.conf — tmux reads it by default.
  # `enter` attaches a tmux session on its own socket (tmux -L sandboxer,
  # see cli tmuxEnterArgs), and a manual `tmux` works the same way:
  # panes reuse the rc.sh launcher for the sandboxer prompt/aliases.
  tmuxConf = pkgs.writeTextDir "etc/tmux.conf" ''
    set -g default-command "bash -c 'test -r /etc/sandboxer/rc.sh && exec bash --rcfile /etc/sandboxer/rc.sh -i || exec bash -i'"
    set -g default-terminal "tmux-256color"
    set -g history-limit 10000
    set -g mouse on
    # Prefix is Ctrl-Space, not the default Ctrl-b — it does not clash with
    # bash's Ctrl-a (beginning of line). e.g. Ctrl-Space c = new window,
    # Ctrl-Space d = detach, Ctrl-Space " / % = split panes.
    set -g prefix C-Space
    unbind C-b
    bind C-Space send-prefix
    set -g status-left '[sbx #{session_name}] '
  '';
in
{
  image = pkgs.dockerTools.buildLayeredImage {
    name = "sandboxer-toolbox";
    tag = "latest";
    maxLayers = 120;
    contents =
      (with pkgs; [
        bashInteractive
        coreutils
        git
        rsync
        jq
        curl
        cacert
        gnused
        gawk
        gnugrep
        openssh
        which
        # tooling pack: pager, editor, process tools, fast search,
        # archives, nicer git diffs, make/unzip — for humans and agents
        less
        neovim
        procps
        ripgrep
        fd
        tree
        gnutar
        gzip
        delta
        gnumake
        unzip
        # everyday language runtimes — baked into the BASE image so
        # scripts and builds just work (the tools packs still exist
        # for pinned per-profile variants). python3 carries a few
        # batteries every glue script reaches for (click CLIs, YAML
        # config, jinja2 templating); the nixpkgs attr is pyyaml, the
        # import is `yaml`.
        (python3.withPackages (ps: with ps; [
          click
          pyyaml
          jinja2
        ]))
        nodejs
        jdk25
        (maven.override { jdk_headless = jdk25; })
        redocly
        # nested containers: ROOTLESS podman-in-podman (never dind —
        # no engine socket is ever mounted) + its runtime pieces;
        # pulls ride the sandbox's HTTP(S)_PROXY through the egress
        # allowlist like any other traffic
        podman
        crun
        conmon
        netavark
        aardvark-dns
        passt
        fuse-overlayfs
        # the multiplexer `enter` attaches (detach/reattach, wheel
        # scrolling, panes) — plus the terminfo it needs
        tmux
        ncurses
      ])
      ++ agentPkgs
      ++ toolPkgs
      ++ [
        shellRc
        gitConfig
        tmuxConf
        containersPolicy
        containersRegistries
        containersStorage
      ]
      ++ userFiles;
    config = {
      # No Entrypoint: the launcher always passes a full command (bash -lc …).
      Cmd = [
        "bash"
        "-l"
      ];
      WorkingDir = "/work";
      # The user's `env` is appended AFTER the defaults: OCI env is
      # applied in list order with the last occurrence winning, so a
      # user variable overrides a same-named default.
      Env = [
        "PATH=/bin"
        "SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt"
        # UTF-8 by default: agent TUIs and tmux need a UTF-8 locale or
        # glyphs degrade to '_' (glibc ships C.UTF-8 unconditionally).
        "LANG=C.UTF-8"
        # Point maven and the JVM tooling at the baked JDK.
        "JAVA_HOME=${pkgs.jdk25.home}"
      ]
      ++ userEnv;
    };
    # /var/tmp is not decoration: containers/image stages every pulled
    # blob there, so the nested podman's first pull dies with
    # "stat /var/tmp: no such file or directory" without it.
    fakeRootCommands = ''
      mkdir -p /work /tmp /var/tmp
      chmod 1777 /tmp /var/tmp
    '';
    enableFakechroot = true; # let npm-agent postinstall scripts run
  };

  # Egress proxy image: a minimal squid enforcing the domain allowlist
  # via our generated /etc/sandboxer/squid.conf (bind-mounted at run
  # time). No sandboxer binary in the network path.
  proxyImage = pkgs.dockerTools.buildLayeredImage {
    name = "sandboxer-proxy";
    tag = "latest";
    contents = [ pkgs.squid ];
    config = {
      Entrypoint = [
        "${pkgs.squid}/bin/squid"
        "-N"
        "-f"
        "/etc/sandboxer/squid.conf"
      ];
      WorkingDir = "/tmp";
    };
    fakeRootCommands = ''
      mkdir -p /tmp /etc/sandboxer
      chmod 1777 /tmp
    '';
    enableFakechroot = true;
  };
}
