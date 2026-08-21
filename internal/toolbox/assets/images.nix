# The toolbox image definition — the SINGLE source of truth for what is in the
# image, imported by both flakes that build it:
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

    # testcontainers (and any docker client) needs the nested podman's
    # docker-compatible API socket; ensure it — idempotent, quiet, detached.
    # The image carries podman-socket; an older cached image without it
    # simply skips (the `command -v` guard), never errors the shell.
    command -v podman-socket >/dev/null 2>&1 && podman-socket >/dev/null 2>&1 || true

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

  # Storage for the nested podman. On a podman engine the launcher
  # mounts generated /etc/subuid+/etc/subgid and grants ambient
  # SETUID/SETGID, so the shipped newuidmap builds a MULTI-uid
  # namespace and normal images unpack (and run their own users)
  # natively. `ignore_chown_errors` stays as the FALLBACK for where
  # that grant does not exist — a docker engine (no ambient caps for a
  # non-root user) or a host user without subordinate ranges — where
  # podman maps a SINGLE uid and unpacking a normal image would
  # otherwise abort on its foreign owners (alpine's /etc/shadow is
  # 0:42); the chown is skipped instead. fuse-overlayfs (not vfs)
  # keeps layers shared; it needs /dev/fuse, which the launcher passes
  # only for a profile that opted in (see backend.nestedContainerArgs).
  containersStorage = pkgs.writeTextDir "etc/containers/storage.conf" ''
    [storage]
    driver = "overlay"
    [storage.options]
    mount_program = "${pkgs.fuse-overlayfs}/bin/fuse-overlayfs"
    ignore_chown_errors = "true"
  '';
  # Engine settings for the nested podman. Deliberately ONE setting: with a
  # compose provider on PATH, `podman compose` prints a four-line banner
  # about executing an external provider before every single command, which
  # is pure noise for an agent parsing output. Everything else podman
  # resolves correctly on its own here — measured inside the sandbox, its
  # defaults already come out k8s-file / file / cgroupfs / netavark, so
  # pinning them would only be a copy of the default that goes stale.
  containersConf = pkgs.writeTextDir "etc/containers/containers.conf" ''
    [engine]
    compose_warning_logs = false
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

  # Guest identity files (/etc/passwd, /etc/group). Container engines never
  # consult them for exec (the uid is numeric), but microsandbox >= 0.6.7
  # resolves the exec user against the GUEST's /etc/passwd, and without these
  # every exec into the machine dies with "failed to resolve guest uid 0".
  # Entries mirror the generated files the nestedContainers container path
  # bind-mounts (internal/sandbox/subid.go) — and those mounts land OVER
  # these, so on that path this pair only ever back-fills.
  guestNss = pkgs.runCommand "guest-nss" { } ''
    mkdir -p $out/etc
    printf 'root:x:0:0:root:/root:/bin/bash\nnobody:x:65534:65534:nobody:/var/empty:/bin/sh\n' > $out/etc/passwd
    printf 'root:x:0:\nnobody:x:65534:\n' > $out/etc/group
  '';

  # pi packages baked into the image, exposed at a STABLE guest path. pi loads
  # a package listed in its settings (`packages: [ … ]`), and a local path
  # entry is taken verbatim — so the path must not move when the image is
  # rebuilt: sandboxer writes it into the sandbox's ~/.pi/agent/settings.json
  # ONCE (sandbox.EnsurePiPackages) and that home outlives any number of image
  # versions. Hence the indirection: the store path changes on every bump, the
  # symlink under /etc/sandboxer/pi-packages/ does not. Keep the leaf names in
  # sync with internal/sandbox/pipkgs.go (the same literal on the Go side).
  #
  # agent-orchestrator = multi-agent orchestration for pi (subagents, swarms,
  # worktree isolation, the /agents dashboard). Registered by default, and a
  # profile can opt out with piPackages = false.
  piPackages = pkgs.runCommand "sandboxer-pi-packages" { } ''
    mkdir -p $out/etc/sandboxer/pi-packages
    ln -s ${pkgs.pi-agent-orchestrator}/lib/node_modules/@groeponline/pi-agent-orchestrator \
      $out/etc/sandboxer/pi-packages/agent-orchestrator
  '';

  # git with a sandbox-awareness guard. A managed source is a HOST worktree:
  # its .git is a pointer FILE whose gitdir names a host path that is not
  # mounted unless the source opted in with git = "ro"/"rw" (by default git
  # does not enter the sandbox — the mount set is the wall). Plain git greets
  # the un-shared case with "fatal: not a git repository", which reads as
  # breakage — and an agent then "repairs" it with `git init`, orphaning the
  # host worktree (the live incident this guard closes). The wrapper explains
  # the design at exactly the moment of confusion, names the key that would
  # change it, and refuses — instead of letting the confusing fatal invite a
  # destructive fix. Everything else — a source whose git dir IS shared (its
  # pointer resolves, so the guard never fires), repos cloned inside the
  # sandbox, plain dirs — passes straight through.
  gitGuarded = pkgs.symlinkJoin {
    name = "git-guarded";
    paths = [ pkgs.git ];
    postBuild = ''
      rm $out/bin/git
      cat > $out/bin/git <<GUARD
      #!${pkgs.runtimeShell}
      d=\$PWD
      while [ -n "\$d" ] && [ "\$d" != "/" ]; do
        if [ -e "\$d/.git" ]; then
          if [ -f "\$d/.git" ]; then
            tgt=\$(${pkgs.gnused}/bin/sed -n 's/^gitdir: //p' "\$d/.git" 2>/dev/null)
            if [ -n "\$tgt" ] && [ ! -e "\$tgt" ]; then
              echo "sandboxer: \$d is a sandboxer-managed git WORKTREE — its git metadata lives on the HOST and is deliberately not mounted here (the mount set is the isolation wall)." >&2
              echo "sandboxer: git cannot operate on this tree from inside the sandbox. Edit the files; committing and reviewing happen on the host." >&2
              echo "sandboxer: do NOT 'git init' here — it would orphan the host worktree and your uncommitted work gets set aside on the next sync." >&2
              echo "sandboxer: to have git here, the HOST owner sets git = \"ro\" (history) or git = \"rw\" (commits) on this source in sandboxer.nix and re-enters." >&2
              exit 128
            fi
          fi
          break
        fi
        d=\$(${pkgs.coreutils}/bin/dirname "\$d")
      done
      exec ${pkgs.git}/bin/git "\$@"
      GUARD
      chmod +x $out/bin/git
    '';
  };

  # `detach` as a COMMAND: the escape hatch when no key reaches tmux (an
  # input-method toggle eating Ctrl-Space, a terminal swallowing Alt-d, a
  # nested multiplexer). Typing `exit` instead ENDS the tmux session and with
  # it whatever the agent was running — this leaves it running, exactly as the
  # prefix binding would.
  detachCmd = pkgs.writeShellScriptBin "detach" ''
    if [ -z "$TMUX" ]; then
      echo "detach: not inside the sandbox's tmux session (nothing to detach from)" >&2
      exit 1
    fi
    exec ${pkgs.tmux}/bin/tmux detach-client
  '';

  # `docker` inside the sandbox — a shim, not the real client. The docker
  # CLI speaks to a daemon over a socket, and no engine socket is ever
  # mounted into a sandbox (that is the whole point: not docker-in-docker),
  # so shipping it would only produce "cannot connect to the daemon".
  # podman's CLI is docker-compatible, so `docker run|build|ps|logs|compose`
  # all land where the user expects — an agent that types docker out of
  # habit just works. A profile that installs a real docker client through
  # image.packages collides with this name; that is the profile's call.
  dockerShim = pkgs.writeShellScriptBin "docker" ''
    exec ${pkgs.podman}/bin/podman "$@"
  '';

  # testcontainers & friends talk to a docker-compatible API SOCKET, never a
  # CLI: without one every testcontainers suite fails with "Could not find a
  # valid Docker environment", and the docker compose shim needs it too. This
  # helper lazily starts the nested podman's API service on the standard
  # docker socket path. Idempotent (the socket check is the fast path),
  # detached (nohup + </dev/null: it must outlive the shell that started it
  # and keep serving the persistent machine between enter/exec calls), and
  # raced safely — a stale pidfile from a previous machine boot is ignored
  # when its pid no longer names a podman process (kill -0 + the comm name),
  # so a reused pid can never read as "already running" with no socket up.
  # Wired into the interactive rc (below) and prefixed onto every exec/run
  # command by the CLI; a sandbox with no podman simply never gets a socket
  # and tools that need one fail on their own, loudly.
  podmanSocket = pkgs.writeShellScriptBin "podman-socket" ''
    # already up — the fast path every shell after the first takes
    [ -S /var/run/docker.sock ] && exit 0
    pid=/var/run/sandboxer-podman.pid
    if [ -f "$pid" ]; then
      p=$(cat "$pid" 2>/dev/null)
      # alive AND a podman process: comm is the executable name, so a reused
      # pid from before a machine restart never reads as "already running"
      if kill -0 "$p" 2>/dev/null && [ "$(cat "/proc/$p/comm" 2>/dev/null)" = podman ]; then
        exit 0
      fi
    fi
    rm -f "$pid"
    mkdir -p /run /var/run /var/log/sandboxer
    nohup podman system service --time=0 unix:///var/run/docker.sock \
      </dev/null >>/var/log/sandboxer/podman-socket.log 2>&1 &
    echo $! > "$pid"
    # wait for the socket to accept connections (podman binds in well under
    # a second; 3s is the worst-case stall when the engine is broken)
    i=0
    while [ $i -lt 30 ]; do
      [ -S /var/run/docker.sock ] && exit 0
      i=$((i+1))
      sleep 0.1
    done
    echo "sandboxer: podman API socket did not come up — see /var/log/sandboxer/podman-socket.log" >&2
    exit 1
  '';

  # System tmux config at /etc/tmux.conf — tmux reads it by default.
  # `enter` attaches a tmux session on its own socket (tmux -L sandboxer,
  # see cli tmuxEnterArgs), and a manual `tmux` works the same way:
  # panes reuse the rc.sh launcher for the sandboxer prompt/aliases.
  tmuxConf = pkgs.writeTextDir "etc/tmux.conf" ''
    # ── behaviour ────────────────────────────────────────────────────────
    set -g default-command "bash -c 'test -r /etc/sandboxer/rc.sh && exec bash --rcfile /etc/sandboxer/rc.sh -i || exec bash -i'"
    set -g default-terminal "tmux-256color"
    # True colour: the palette below is 24-bit, and without this tmux
    # quantizes every hex to the nearest xterm-256 slot (muddy, banded).
    set -as terminal-features ",*:RGB"
    set -ga terminal-overrides ",*256col*:Tc"
    # Modified keys (Shift-Enter, Ctrl-Enter, Ctrl-Shift-*) reach the agent
    # only if tmux both ADVERTISES the capability and is told to emit it.
    # Without this an agent TUI cannot tell Enter from Shift-Enter — Claude
    # Code says so on startup ("tmux extended-keys is off. Modified Enter
    # keys may not work"), and a newline-in-prompt binding silently submits
    # instead. csi-u, not xterm: this tmux is nested inside the operator's
    # own multiplexer, and the CSI-u encoding is the one that survives the
    # outer layer intact.
    set -as terminal-features ",*:extkeys"
    set -s  extended-keys on
    set -g  extended-keys-format csi-u
    set -g history-limit 50000
    set -g mouse on
    set -g base-index 1
    setw -g pane-base-index 1
    set -g renumber-windows on
    set -g focus-events on
    # ESC must be ESC, not the head of a maybe-escape-sequence: the default
    # 500ms wait makes vim/agent TUIs feel broken inside tmux.
    set -sg escape-time 10
    # Let the inner program talk to the OUTER terminal: OSC 52 puts a yank
    # from inside the sandbox on the host clipboard, and passthrough lets
    # image/hyperlink sequences survive the multiplexer.
    set -g set-clipboard on
    set -g allow-passthrough on
    set -g display-time 2000
    set -g status-interval 5
    # Prefix is Ctrl-Space, not the default Ctrl-b — it does not clash with
    # bash's Ctrl-a (beginning of line). e.g. Ctrl-Space c = new window,
    # Ctrl-Space d = detach, Ctrl-Space " / % = split panes.
    set -g prefix C-Space
    # C-b stays as the SECOND prefix rather than being unbound: Ctrl-Space is
    # a popular input-method toggle (ibus/GNOME/KDE), and a desktop that eats
    # it used to leave no way to detach — with `exit` ending the session, that
    # is a trap, not an inconvenience. Whichever prefix reaches tmux works.
    set -g prefix2 C-b
    bind C-Space send-prefix
    # Detach without any prefix at all, for a terminal that swallows both.
    bind -n M-d detach-client
    # Reload without leaving the sandbox.
    bind r source-file /etc/tmux.conf \; display-message "tmux.conf reloaded"

    # Hoist the sandbox slug into a tmux user option ONCE at server start.
    # run-shell inherits the server's environment (a status-bar #() does
    # NOT — it runs detached from it), and doing it here costs one fork
    # instead of one per status refresh, per client, forever.
    run-shell 'tmux set -g @sbx "''${SANDBOXER_SLUG:-sandbox}"'

    # ── look: Catppuccin Mocha, flat ─────────────────────────────────────
    # Deliberately NO powerline separators. Those glyphs (U+E0B0 and
    # friends) live in the Unicode Private Use Area, so they only render
    # with a patched Nerd Font — and the HOST terminal's font is not ours
    # to choose. Everything drawn here is Block Elements or ASCII, which
    # every monospace font ships, so the bar looks the same everywhere
    # instead of degrading into a row of tofu boxes.
    set -g status on
    set -g status-position bottom
    set -g status-justify left
    set -g status-style "bg=#181825,fg=#a6adc8"
    set -g status-left-length 60
    set -g status-right-length 60

    # Left: WHICH SANDBOX you are in — the one fact a shell in here must
    # never leave ambiguous — and a prefix indicator, so Ctrl-Space is
    # never a guess: the block flips to peach the moment the prefix is
    # armed and back on the next key.
    set -g status-left "#{?client_prefix,#[bg=#fab387]#[fg=#11111b]#[bold] ▌ PREFIX ,#[bg=#cba6f7]#[fg=#11111b]#[bold] ▌ sbx #{@sbx} }#[bg=#181825]#[fg=#585b70,none] #{session_name} "

    # Windows: the current one carries the lavender bar, the rest recede.
    set -g window-status-separator ""
    set -g window-status-format "#[fg=#6c7086,bg=#181825] #{window_index}·#{window_name} "
    set -g window-status-current-format "#[fg=#11111b,bg=#b4befe,bold] #{window_index}·#{window_name} "
    set -g window-status-activity-style "fg=#f9e2af,bg=#181825,none"
    set -g window-status-bell-style "fg=#f38ba8,bg=#181825,bold"

    # Right: clock only. No battery/CPU/network widgets — each one is a
    # shell-out every few seconds inside somebody's sandbox, and none of
    # them tells you anything the host's own bar does not.
    set -g status-right "#[fg=#585b70] %H:%M #[fg=#11111b,bg=#89b4fa,bold] %d %b "

    # Panes: the active border glows, the idle ones sink into the bg.
    set -g pane-border-style "fg=#313244"
    set -g pane-active-border-style "fg=#b4befe"
    set -g pane-border-lines heavy

    set -g message-style "bg=#cba6f7,fg=#11111b,bold"
    set -g message-command-style "bg=#313244,fg=#cdd6f4"
    set -g mode-style "bg=#b4befe,fg=#11111b,bold"
    set -g display-panes-active-colour "#cba6f7"
    set -g display-panes-colour "#585b70"
    set -g set-titles on
    set -g set-titles-string "sbx #{@sbx} · #{window_name}"
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
        rsync
        jq
        curl
        cacert
        gnused
        gawk
        gnugrep
        # findutils: `find` and `xargs`. NOT part of coreutils, and fd/rg do
        # not stand in for them — an agent (and half the shell snippets on the
        # internet) reflexively types `find . -name … -exec` or pipes into
        # `xargs`, and a sandbox without them answers "command not found" to
        # the most basic file walk there is. Reported live by dsh, which fell
        # back to `tree` to enumerate a tree.
        findutils
        openssh
        which
        # tooling pack: pager, editor, process tools, fast search,
        # archives, nicer git diffs, make/unzip — for humans and agents
        less
        neovim
        procps
        # glibc's getent — NSS-aware name/identity lookups for agents and the
        # e2e suite's resolve probes (busybox images carry their own).
        getent
        ripgrep
        fd
        tree
        gnutar
        gzip
        delta
        gnumake
        unzip
        # comparison pack. `diff` is NOT part of coreutils, so a sandbox
        # without diffutils answered the single most reflexive command in
        # this family with "command not found" — and delta only colors
        # git's OWN diffs, which a sandbox does not have by default (git
        # enters only when a source opts in). diffutils brings
        # diff/cmp/diff3/sdiff (cmp is also the binary-file answer), patch
        # applies what a diff produced, difftastic (difft) diffs by SYNTAX
        # rather than by line — the one that survives reformatting and
        # tells a moved block from a changed one — and dyff compares YAML
        # and JSON structurally, pairing with the yq-go below for config
        # and manifest work.
        diffutils
        patch
        difftastic
        dyff
        # source-code pack: what an agent needs to READ and CHANGE an
        # unfamiliar tree, none of which the language runtimes below bring.
        # ast-grep matches and REWRITES by syntax tree across languages
        # (`ast-grep -p 'foo($A)' -r 'bar($A)'`) — the one mechanical-refactor
        # tool that cannot mangle a string literal or a comment the way a
        # regex sweep does; universal-ctags builds the symbol index neovim
        # already knows how to jump through (:tag, C-]) so navigation needs
        # no language server; tokei answers "what IS this repo" in one
        # command before any of that; bat prints a file with syntax colors
        # AND line numbers, which is how an agent cites a location; fzf
        # filters candidate paths/symbols (`fzf -f` is non-interactive, so it
        # works in a pipeline, not only under a human); entr reruns a command
        # when files change, the test loop an agent otherwise fakes with
        # sleep; and ruff lints/formats python with zero config — baked for
        # the same reason shellcheck is, python3 being right below. Tools
        # that need per-project configuration (eslint and friends) stay with
        # the project's own dependencies.
        ast-grep
        universal-ctags
        tokei
        bat
        fzf
        entr
        ruff
        # LLM-agent batteries: the everyday tools an agent reaches for and
        # used to hit "command not found" on — network/egress forensics
        # (the allowlist is NAME-bound, so "why can't I reach X" starts with
        # dig/ip/ping/nc), YAML/JSON config editing (yq), artifact and binary
        # inspection (file/binutils/xxd), archive creation (zip), in-place
        # edits that survive a busy file (moreutils' sponge), shell linting
        # for the scripts agents write (shellcheck), port/file holder
        # forensics (lsof), TLS debugging (openssl s_client), and the GitHub
        # CLI for PRs/issues (auth: GH_TOKEN in the profile env, or
        # `gh auth login` — the default egress allowlist covers api.github.com).
        bind.dnsutils
        iproute2
        iputils
        netcat-openbsd
        yq-go
        file
        binutils
        xxd
        zip
        moreutils
        shellcheck
        lsof
        openssl
        gh
        # everyday language runtimes — baked into the BASE image so
        # scripts and builds just work (the tools packs still exist
        # for pinned per-profile variants). python3 carries the batteries
        # a glue script or an agent reaches for; the nixpkgs attr is
        # pyyaml, the import is `yaml`.
        #
        # The set is chosen by "what cannot be improvised": a test runner
        # (pytest), round-trip config editing that PRESERVES comments and
        # layout — ruamel-yaml for YAML and tomlkit for TOML, where pyyaml
        # and tomllib would silently rewrite a pyproject.toml or a manifest
        # into unrecognizable shape — schema validation (jsonschema), HTML
        # parsing (beautifulsoup4 + the lxml backend it wants), modern
        # HTTP with async and HTTP/2 alongside requests (httpx), the date
        # parsing everyone reimplements badly (python-dateutil), and
        # readable terminal output (rich). Together +49 MB over the
        # previous four.
        #
        # numpy/pandas are deliberately NOT here: they would add ~334 MB
        # to every sandbox for a need that is occasional. That is what uv
        # below is for — pypi.org and files.pythonhosted.org are in the
        # default egress allowlist, so `uv venv && uv pip install pandas`
        # works inside a sandbox out of the box, in seconds.
        (python3.withPackages (
          ps: with ps; [
            click
            pyyaml
            jinja2
            requests
            pytest
            rich
            httpx
            tomlkit
            ruamel-yaml
            jsonschema
            beautifulsoup4
            lxml
            python-dateutil
          ]
        ))
        # The escape hatch for every python package NOT baked above (and
        # for a project that pins its own): uv creates a venv and installs
        # into it in seconds, without touching the read-only nix store the
        # baked interpreter lives in — which is why plain `pip install`
        # cannot work here and an agent must not be left to discover that
        # by failing.
        uv
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
        # newuidmap/newgidmap for the nested podman's MULTI-uid namespace.
        # No setuid bit and none needed: on a podman engine the launcher
        # grants SETUID/SETGID as AMBIENT caps (survive execve under
        # no-new-privileges), which is all the maps take to write.
        shadow
        # `podman compose` / `docker compose` need an external provider;
        # podman finds this one on PATH.
        podman-compose
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
        dockerShim
        detachCmd
        podmanSocket
        gitGuarded
        guestNss
        piPackages
        containersPolicy
        containersRegistries
        containersStorage
        containersConf
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
        # testcontainers reads DOCKER_HOST for the engine endpoint; the socket
        # itself is started lazily by podman-socket (see above).
        "DOCKER_HOST=unix:///var/run/docker.sock"
        # Ryuk, testcontainers' reaper sidecar, is the #1 podman failure mode
        # (privileged container + docker-socket mount assumptions); the
        # sandbox machine itself is the cleanup boundary — sandboxer rm/clean
        # wipes everything a test run leaves behind.
        "TESTCONTAINERS_RYUK_DISABLED=true"
      ]
      ++ userEnv;
    };
    # /var/tmp is not decoration: containers/image stages every pulled
    # blob there, so the nested podman's first pull dies with
    # "stat /var/tmp: no such file or directory" without it.
    fakeRootCommands = ''
      mkdir -p /work /tmp /var/tmp /root /var/empty
      chmod 1777 /tmp /var/tmp
      chmod 700 /root
    '';
    enableFakechroot = true; # let npm-agent postinstall scripts run
  };
}
