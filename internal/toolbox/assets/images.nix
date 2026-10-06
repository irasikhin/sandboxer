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
# in the embedded flake, so `.#image` quietly lost the language runtimes and the
# shell rc, and the e2e suite tested an image no user ever gets. Keep
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

    # testcontainers (and any docker client) needs the guest's Docker
    # daemon; ensure it — idempotent, quiet, detached. The image carries
    # docker-daemon; an older cached image without it simply skips (the
    # `command -v` guard), never errors the shell.
    command -v docker-daemon >/dev/null 2>&1 && docker-daemon >/dev/null 2>&1 || true

    # OOM watchdog. A microVM has no swap, so the machine's memory cap
    # (profile limits.memory / SANDBOXER_MEM) is a hard ceiling: the guest
    # kernel kills the biggest process when it is exceeded, and the shell
    # reports that as a bare "Killed" with no hint of why. The kill is
    # recorded in /dev/kmsg, so the first interactive shell after an
    # incident names the cap and the knob that raises it. The marker is
    # per-boot (tmpfs) on purpose: a reboot is a fresh machine and a fresh
    # warning is wanted.
    if [ ! -e /tmp/sandboxer-oom-seen ] && [ -r /dev/kmsg ] && \
       timeout 0.3 cat /dev/kmsg 2>/dev/null | grep -qi "oom-killer"; then
      cap=$(awk '/^MemTotal:/ {print int($2/1024) " MiB"}' /proc/meminfo 2>/dev/null)
      printf '\033[1;31m⚠ sandboxer: the kernel OOM-killed a process in this machine (memory cap %s).\033[0m\n' "''${cap:-unknown}"
      printf '  raise it in the profile (limits.memory / SANDBOXER_MEM), then restart the workload.\n'
      : > /tmp/sandboxer-oom-seen
    fi

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

  # Guest identity files (/etc/passwd, /etc/group). Container engines never
  # consult them for exec (the uid is numeric), but microsandbox >= 0.6.7
  # resolves the exec user against the GUEST's /etc/passwd, and without these
  # every exec into the machine dies with "failed to resolve guest uid 0".
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

  # helm plugins baked into the image, exposed at a STABLE guest path for the
  # same reason piPackages is: the store path behind the plugin moves on every
  # image bump, the sandbox home that names it does not.
  #
  # Unlike pi, helm cannot be told where to look from a system file: HELM_PLUGINS
  # names ONE directory and its default is the user's own data dir
  # (~/.local/share/helm/plugins) — there is no /etc-level plugin dir the image
  # could drop a file into. So the image only SHIPS the plugin here and
  # internal/sandbox/helmplugins.go links it into the sandbox home on
  # create/enter/exec. Keep the leaf names in sync with sandbox.BakedHelmPlugins
  # (the same literals on the Go side).
  #
  # helm-diff is the one helmfile shells out to for `helmfile diff`/`apply`, so
  # baking it is what makes those commands work with zero setup — a
  # `helm plugin install` inside a sandbox would need network and a writable
  # plugin dir.
  helmPlugins = pkgs.runCommand "sandboxer-helm-plugins" { } ''
    mkdir -p $out/etc/sandboxer/helm-plugins
    ln -s ${pkgs.kubernetes-helmPlugins.helm-diff}/helm-diff \
      $out/etc/sandboxer/helm-plugins/helm-diff
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

  # `java` and friends ON PATH. The JDK's own $out/bin is a SYMLINK to
  # lib/openjdk/bin, and the layered-image merge resolves it into
  # ./lib/openjdk/bin/… without ever creating ./bin/java — so with PATH=/bin
  # the image had a JDK (JAVA_HOME, mvn) but no `java` at all (verified by
  # tarball inspection). Real per-tool symlinks land where PATH looks.
  jdkBin = pkgs.runCommand "jdk-bin" { } ''
    mkdir -p $out/bin
    for f in ${pkgs.jdk25}/lib/openjdk/bin/*; do
      ln -s "$f" "$out/bin/$(basename "$f")"
    done
  '';

  # pkl (Apple's configuration language) on the image's OWN JDK. nixpkgs' pkl
  # is a JVM build and its runtime JRE (temurin-bin-21) alone is ~639 MiB — a
  # third of the whole image — while the image already bakes jdk25. So the jar
  # is copied out of the package (it carries no store refs, so the temurin
  # closure drops entirely) and wrapped against the baked JDK. The two JVM
  # flags silence the JNA / sun.misc.Unsafe warnings Java 25 otherwise prints
  # on every invocation. `pkl eval` works offline; `package://` imports fetch
  # from pkg.pkl-lang.org, which the default egress allowlist carries.
  pklCli = pkgs.runCommand "pkl" { nativeBuildInputs = [ pkgs.makeWrapper ]; } ''
    mkdir -p $out/bin $out/opt/pkl
    cp ${pkgs.pkl}/opt/pkl/jpkl.jar $out/opt/pkl/jpkl.jar
    makeWrapper ${pkgs.jdk25}/bin/java $out/bin/pkl \
      --add-flags "--enable-native-access=ALL-UNNAMED" \
      --add-flags "--sun-misc-unsafe-memory-access=allow" \
      --add-flags "-jar $out/opt/pkl/jpkl.jar"
  '';

  # The standard dynamic-loader paths, for binaries the nix store does NOT
  # own. A nix-patched ELF never asks for this path (its interpreter is a
  # store path), but a FOREIGN dynamically-linked binary — garden's
  # self-extracted node runtime, a krew-installed kubectl plugin — names
  # /lib64/ld-linux-x86-64.so.2 (x86_64) or /lib/ld-linux-aarch64.so.1
  # (aarch64), and the image has neither. Those interpreters resolve libc/
  # libm/… from their own store system-dirs, so the interpreter path is the
  # ONLY thing missing here — which is why one symlink is enough. glibc is
  # named explicitly rather than via pkgs.glibcLocales etc.: this IS the
  # loader the compat path must point at.
  loaderCompat = pkgs.runCommand "foreign-loader-compat" { } (
    if pkgs.stdenv.hostPlatform.isx86_64 then
      ''
        mkdir -p $out/lib64 $out/lib
        ln -s ${pkgs.glibc}/lib64/ld-linux-x86-64.so.2 $out/lib64/ld-linux-x86-64.so.2
        ln -s ${pkgs.glibc}/lib/ld-linux-x86-64.so.2 $out/lib/ld-linux-x86-64.so.2
      ''
    else if pkgs.stdenv.hostPlatform.isAarch64 then
      ''
        mkdir -p $out/lib
        ln -s ${pkgs.glibc}/lib/ld-linux-aarch64.so.1 $out/lib/ld-linux-aarch64.so.1
      ''
    else
      throw "sandboxer: no loader-compat paths for ${pkgs.stdenv.hostPlatform.system}"
  );

  # The garden CLI on PATH. The launcher itself is nix-patched (see
  # assets/garden/package.nix), but the node runtime it EXTRACTS on first run
  # is a generic foreign ELF: libstdc++ (the gcc lib output) is not among the
  # loader's own system dirs, so it must arrive via LD_LIBRARY_PATH. --prefix,
  # not --set: a caller's own LD_LIBRARY_PATH keeps winning.
  gardenCli = pkgs.runCommand "garden-cli" { nativeBuildInputs = [ pkgs.makeWrapper ]; } ''
    mkdir -p $out/bin
    makeWrapper ${pkgs.garden}/bin/garden $out/bin/garden \
      --prefix LD_LIBRARY_PATH : ${pkgs.stdenv.cc.cc.lib}/lib
  '';

  # `pip` that explains itself. The baked interpreter lives in the read-only
  # nix store, so pip cannot install into it — and nixpkgs' python does not
  # ship pip at all, which leaves an agent with "command not found" (or "No
  # module named pip") and no idea what to do instead. uv is the answer, in
  # the sandbox's own directory, and the default egress allowlist already
  # covers pypi.
  pipHint = pkgs.writeShellScriptBin "pip" ''
    echo "sandboxer: this python lives in the read-only nix store — pip cannot install into it." >&2
    echo "sandboxer: use uv instead (pypi.org is in the default egress allowlist):" >&2
    echo "sandboxer:   uv venv && . .venv/bin/activate && uv pip install <pkg>" >&2
    echo "sandboxer: or, for a one-off run:  uv run --with <pkg> python -c '...'" >&2
    echo "sandboxer: the baked interpreter already carries pytest, httpx, rich, ruamel-yaml, tomlkit, jsonschema, bs4." >&2
    exit 1
  '';

  # `docker compose` and `docker buildx` as CLI plugins. nixpkgs patches the
  # docker client so its SYSTEM plugin dirs come only from the
  # DOCKER_CLI_PLUGIN_DIRS env var (the nix wrapper exports the dirs of the
  # plugins it was built with), which means the client's own documented
  # system dir is searched only when that variable names it too. The plugins
  # are placed at that path and the image env below points the variable at
  # it: `docker compose`/`docker buildx` then resolve no matter how the
  # client is reached (a script that drops the wrapper's env, a future
  # client build), and the hyphenated `docker-compose` stays a bare command
  # on PATH (its package ships bin/docker-compose).
  dockerPlugins = pkgs.runCommand "docker-cli-plugins" { } ''
    mkdir -p $out/usr/libexec/docker/cli-plugins
    ln -s ${pkgs.docker-compose}/bin/docker-compose $out/usr/libexec/docker/cli-plugins/docker-compose
    ln -s ${pkgs.docker-buildx}/bin/docker-buildx $out/usr/libexec/docker/cli-plugins/docker-buildx
  '';

  # testcontainers & friends talk to a docker-compatible API SOCKET, never a
  # CLI: without one every testcontainers suite fails with "Could not find a
  # valid Docker environment", and the compose plugin needs it too. This
  # helper starts the guest's OWN Docker daemon on the standard socket path —
  # no host daemon socket is ever shared into a sandbox. Idempotent (the
  # socket check is the fast path), detached (nohup + </dev/null: it must
  # outlive the shell that started it and keep serving the persistent machine
  # between enter/exec calls), and raced safely — a stale pidfile from a
  # previous machine boot is ignored when its pid no longer names a dockerd
  # process (kill -0 + the comm name), so a reused pid can never read as
  # "already running" with no socket up. Wired into the interactive rc (above)
  # and prefixed onto every exec/run command by the CLI.
  #
  # The daemon's data-root is /var/lib/docker: the ext4 volume the backend
  # mounts there (--mount-owned … kind=disk, limits.dockerDisk), because
  # Docker's overlay storage cannot live on the guest's overlayfs root
  # (measured: the containerd snapshotter's mount fails with EINVAL there).
  # A machine without the volume is unusable, so the fs-type check below names
  # that instead of letting the user meet the opaque mount error later.
  dockerDaemon = pkgs.writeShellScriptBin "docker-daemon" ''
    # already up — the fast path every shell after the first takes. A socket
    # FILE alone proves nothing: a SIGKILLed daemon leaves the file behind and
    # every later ensure would then lie. One `docker info` (~100 ms against a
    # live daemon) is the cheap liveness proof.
    if [ -S /var/run/docker.sock ] && { ! command -v docker >/dev/null 2>&1 || docker info >/dev/null 2>&1; }; then
      exit 0
    fi
    # no data volume: /var/lib/docker would land on the guest's overlayfs root
    mkdir -p /var/lib/docker 2>/dev/null || true
    case "$(stat -f -c %T /var/lib/docker 2>/dev/null)" in
      overlay|overlayfs)
        echo "sandboxer: /var/lib/docker is on the guest's overlayfs root — this machine has no Docker data volume." >&2
        echo "sandboxer: recreate the sandbox (sandboxer rm <slug>, then create/enter again) so it gets its dockerDisk volume." >&2
        exit 1
        ;;
    esac
    pid=/var/run/sandboxer-dockerd.pid
    if [ -f "$pid" ]; then
      p=$(cat "$pid" 2>/dev/null)
      # alive AND a dockerd process: comm is the executable name, so a reused
      # pid from before a machine restart never reads as "already running"
      if kill -0 "$p" 2>/dev/null && [ "$(cat "/proc/$p/comm" 2>/dev/null)" = dockerd ]; then
        exit 0
      fi
    fi
    rm -f "$pid"
    mkdir -p /run /var/run /var/log/sandboxer
    # a dead daemon also leaves its socket file behind — clear it so the new
    # listener binds on a clean path
    rm -f /var/run/docker.sock
    nohup dockerd </dev/null >>/var/log/sandboxer/dockerd.log 2>&1 &
    echo $! > "$pid"
    # wait for the daemon to SERVE: the socket file appears when the API
    # listener binds, `docker info` only answers once it is up (dockerd boots
    # containerd and the runtime first, so this takes seconds). 15s is the
    # worst-case stall before we stop waiting and say what failed.
    i=0
    while [ $i -lt 150 ]; do
      if [ -S /var/run/docker.sock ] && \
         { ! command -v docker >/dev/null 2>&1 || docker info >/dev/null 2>&1; }; then
        exit 0
      fi
      kill -0 "$(cat "$pid" 2>/dev/null)" 2>/dev/null || break
      i=$((i+1))
      sleep 0.1
    done
    echo "sandboxer: the Docker daemon did not come up — see /var/log/sandboxer/dockerd.log" >&2
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
        # The rest of the POSIX/base-system userland — everything a shell
        # script or an agent assumes exists on any Linux box and that neither
        # coreutils nor the packs below carry. Placed EARLY on purpose: the
        # image layer is assembled by copying each entry over the previous one
        # (dockerTools rsyncs them in list order), so a name that already comes
        # from a later entry — procps' kill, for one — keeps winning.
        # This pack only fills gaps.
        #
        #   util-linux — the single biggest one: column, rev, hexdump/hd,
        #     script (record a session), flock (the shell's only real lock),
        #     setsid/unshare/nsenter, uuidgen, getopt, look, cal, logger,
        #     more, whereis, namei, rename, hardlink, mountpoint, taskset,
        #     ionice, renice, prlimit, lsblk/findmnt/lsns/lsfd, dmesg, ipcs.
        #   psmisc — pstree, killall, fuser (who holds this file/port).
        #   nettools — hostname, netstat, ifconfig, route, arp: iproute2 is
        #     the modern spelling, but half the scripts (and agents) still
        #     type the classic ones.
        #   bzip2/xz/zstd/cpio/p7zip — the archive formats tar meets in the
        #     wild (.tar.bz2/.tar.xz/.tar.zst, .cpio, .7z); gzip, tar and
        #     zip/unzip are already above.
        #   wget — the other download verb every README uses.
        #   bc — arithmetic in a shell (dc rides along).
        #   time — `command time -v` for max RSS; bash's builtin cannot.
        #   gettext — envsubst, the template step of every deploy script.
        #   ed — the POSIX line editor scripts drive non-interactively.
        #   nano — an editor a human can leave without knowing vim.
        #   htop/ncdu/pv — what is running, what ate the disk, how far along.
        #   socat — the swiss-army pipe next to nc.
        #   dos2unix — CRLF files from a Windows checkout.
        #   sqlite — read the .db an unfamiliar tool left behind.
        #   strace — why a syscall failed; a real kernel here, so it works.
        #   whois — domain lookups next to dig.
        #   rlwrap — line editing for a REPL that has none.
        #   man-db + man-pages — `man 2 open`, `man 7 signal`, and the pages
        #     the baked tools ship; tzdata makes TZ= work in date/python.
        util-linux
        psmisc
        nettools
        bzip2
        xz
        zstd
        cpio
        p7zip
        wget
        bc
        time
        gettext
        ed
        nano
        htop
        ncdu
        pv
        socat
        dos2unix
        sqlite
        strace
        whois
        rlwrap
        man-db
        man-pages
        # groff — man's renderer, here for its own sake: nroff/tbl/eqn/pic as
        # commands. man-db does NOT need it in contents (it calls groff by
        # store path), and adding it does not silence groff's cosmetic
        # `cannot select font 'CW'` warning on stderr — measured both ways;
        # the page itself renders correctly either way.
        groff
        tzdata
        openssh
        which
        # tooling pack: pager, editor, process tools, fast search,
        # archives, nicer git diffs, make/unzip — for humans and agents
        less
        # `vi` and `vim` too: scripts, git and half of muscle memory call
        # those names, and plain `neovim` installs only `nvim`.
        (neovim.override {
          viAlias = true;
          vimAlias = true;
        })
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
        # jj (Jujutsu): the git-compatible VCS agents increasingly reach for
        # (`jj status`/`jj new`/`jj squash`, git-repo-backed). Same wall as
        # git — a managed source's history lives on the HOST, so a colocated
        # `jj` works only once the source opts in with git = "ro"/"rw"; a
        # standalone `jj git init` (no --colocate) keeps its store under
        # ./.jj/, which jj itself git-ignores, so it never leaks into the
        # host worktree.
        jujutsu
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
        traceroute
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
        # The container engine: the REAL Docker — the guest's own daemon
        # (dockerd from moby, with containerd, runc, the shim, docker-proxy
        # and docker-init) plus the docker client. No host engine socket is
        # ever mounted into a sandbox and no dind machinery exists: the VM is
        # the boundary, and the engine runs on the microVM's own kernel.
        # nixpkgs' moby wrapper puts its libexec/docker, iptables and
        # iproute2 on dockerd's PATH, so the daemon is self-sufficient; the
        # iptables/nftables packages ride along for the guest shell (Docker
        # 29 programs the bridge with nftables). The daemon's data-root is
        # /var/lib/docker — the ext4 volume the CLI mounts (see dockerDaemon
        # above), never the guest's overlayfs root, which cannot host
        # Docker's overlay storage.
        docker
        # The daemon package behind pkgs.docker (nixpkgs exposes it as
        # docker.moby): pkgs.docker's bin/dockerd is already a symlink to this
        # wrapper, and naming the package keeps the engine's own pieces
        # (dockerd, docker-proxy, the systemd units) explicit in the closure.
        pkgs.docker.moby
        docker-compose
        docker-buildx
        iptables
        nftables
        # local-kubernetes pack: boot a real cluster INSIDE the sandbox and
        # drive it. The clients — kubectl (the API), helm (charts), kustomize
        # (overlays), kubectx/kubens (context and namespace switching), stern
        # (multi-pod log tailing), kubeconform (schema-validate rendered
        # manifests with no cluster at all) and k9s (the TUI) — the helm
        # workflow (helmfile renders a chart set, vals resolves the
        # `${ref+…}` value/secret references it templates into), the GitOps
        # clients (argocd, flux, kubeseal), the kubectl UX and plugin family
        # (kubecolor, tree, ktop, view-secret, images), the standalone
        # access-matrix CLI rakkess, and the
        # cluster-free validators (kube-linter, popeye, pluto next to
        # kubeconform) — plus the two runners:
        #
        # kind — nodes as privileged containers on the guest's own Docker
        #     daemon, which is kind's default provider (no env pins): the node
        #     runs its containerd against Docker's ext4-backed storage, and
        #     the DEFAULT overlayfs snapshotter works there — measured on the
        #     Docker engine: `kind create cluster` reached control-plane Ready
        #     with all 9 kube-system pods Running, with no KIND_EXPERIMENTAL_*
        #     env at all (the podman-era provider and fuse-overlayfs pins are
        #     gone). kind bind-mounts /lib/modules read-only into every node;
        #     the empty dir in fakeRootCommands is belt-and-braces (Docker
        #     creates a missing bind source itself) and the guest ships no
        #     module tree and needs none (the netfilter pieces kube-proxy
        #     programs are built into its kernel).
        #   k3d — k3s containers through the same Docker socket (DOCKER_HOST,
        #     brought up by docker-daemon). Measured on the Docker engine: the
        #     node reaches Ready and every kube-system pod settles (the two
        #     helm-install jobs Completed, the rest Running), and a workload
        #     pulls from docker.io through k3s's containerd and runs. Caveat:
        #     the guest kernel has no physdev iptables match, so k3s's
        #     network-policy controller logs iptables-restore errors for
        #     KUBE-ROUTER-FORWARD — cluster and workloads are unaffected, but
        #     NetworkPolicy enforcement is unverified here.
        #
        # Image pulls ride the default egress allowlist. Large Docker Hub
        # layers can STALL in the guest on some networks (measured for
        # kindest/node and rancher/k3s: the layer download goes silent; the
        # same tags pull fine from mirror.gcr.io, which the defaults already
        # allow) — `docker pull mirror.gcr.io/kindest/node:<tag>` together
        # with `kind create --image …` is the workaround.
        #
        # The host's docker or Kubernetes is never in reach: a node is a
        # container in the GUEST, on the guest's own kernel, and dies with the
        # machine.
        #
        # helmfile's diff step is helm-diff, which helm must find as an
        # INSTALLED plugin in its plugin dir (HELM_PLUGINS or the home's
        # ~/.local/share/helm/plugins). The image ships it at the stable path
        # /etc/sandboxer/helm-plugins/ (helmPlugins above) and the CLI links it
        # into the sandbox home on create/enter/exec
        # (sandbox.EnsureHelmPlugins, killed by SANDBOXER_NO_HELM_PLUGINS=1), so
        # `helmfile diff` and `helmfile apply` work with zero setup — no
        # `helm plugin install` (which would need network and a writable plugin
        # dir).
        kubectl
        kubernetes-helm
        kustomize
        # the helm workflow: helmfile renders a chart set from a values/secrets
        # hierarchy, vals is the value backend that resolves the `${ref+…}`
        # references (Vault, cloud secret managers, plain env) in it
        helmfile
        vals
        # GitOps clients: argocd and flux drive a cluster from a git repo;
        # kubeseal turns a plaintext Secret into a SealedSecret the cluster-side
        # controller decrypts
        argocd
        fluxcd
        kubeseal
        # kubectl UX and plugin family. Every kubectl-* binary lands on /bin,
        # the image's whole PATH, which is the only thing kubectl's plugin
        # lookup needs — and it also accepts the underscore spelling, so
        # `kubectl view-secret` finds kubectl-view_secret (the name its package
        # ships)
        kubecolor
        kubectl-tree
        # rakkess — a standalone access-matrix CLI; its package ships no
        # kubectl- prefixed binary (so it is `rakkess`, never `kubectl rakkess`)
        rakkess
        # `kubectl ktop` — nixpkgs' attr is kubectl-ktop (the deprecated `ktop`
        # alias resolves to the same package and only adds a rename warning to
        # every eval) and it ships both the ktop and kubectl-ktop binaries
        kubectl-ktop
        kubectl-view-secret
        kubectl-images
        kind
        k3d
        kubectx
        stern
        kubeconform
        # cluster-free validators, next to kubeconform: kube-linter (lint
        # rendered manifests), popeye (score a LIVE cluster's resources) and
        # pluto (deprecated/removed APIs a manifest or a cluster still uses)
        kube-linter
        popeye
        pluto
        k9s
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
        dockerPlugins
        jdkBin
        pklCli
        pipHint
        detachCmd
        dockerDaemon
        gitGuarded
        guestNss
        piPackages
        helmPlugins
        # garden (the wrapped CLI) and the standard loader paths its
        # self-extracted node runtime needs — deliberately HERE, not in the
        # k8s pack above, which must not get the unwrapped pkgs.garden.
        gardenCli
        loaderCompat
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
        # Where the baked packages' man pages actually live. The image merges
        # every package at the root, so the pages are /share/man — a path no
        # man-db default mentions, which is why `man socat` answered "no manual
        # entry" with 577 pages sitting right there (measured inside the VM).
        "MANPATH=/share/man"
        # Same story for the timezone database: glibc looks in
        # /usr/share/zoneinfo, the image has /share/zoneinfo, so TZ= was
        # silently ignored (TZ=Asia/Tokyo date +%Z printed "Asia", not "JST").
        "TZDIR=/share/zoneinfo"
        # Point maven and the JVM tooling at the baked JDK.
        "JAVA_HOME=${pkgs.jdk25.home}"
        # testcontainers reads DOCKER_HOST for the engine endpoint; the daemon
        # itself is started by docker-daemon (see above).
        "DOCKER_HOST=unix:///var/run/docker.sock"
        # The system plugin dir the nixpkgs docker client was patched to read
        # (see dockerPlugins above): the wrapper already exports the store
        # dirs of the plugins it was built with, and this adds the image's own
        # dir, so `docker compose`/`docker buildx` resolve however the client
        # is reached.
        "DOCKER_CLI_PLUGIN_DIRS=/usr/libexec/docker/cli-plugins"
        # Ryuk, testcontainers' reaper sidecar, wants a privileged container
        # plus docker-socket mount assumptions that do not hold inside a
        # sandbox; the sandbox machine itself is the cleanup boundary —
        # sandboxer rm/clean wipes everything a test run leaves behind.
        "TESTCONTAINERS_RYUK_DISABLED=true"
      ]
      ++ userEnv;
    };
    # /var/tmp is not decoration: the engine and BuildKit stage pulled blobs
    # and build context somewhere, and a missing /var/tmp breaks a pull or a
    # build with "stat /var/tmp: no such file or directory".
    # /lib/modules: every kind node bind-mounts it read-only, and the empty
    # dir keeps that bind source present as belt-and-braces (Docker creates a
    # missing bind source itself). The guest ships no module
    # tree and needs none — the netfilter pieces kube-proxy programs are built
    # into its kernel (measured), so an empty dir is the whole content.
    fakeRootCommands = ''
      mkdir -p /work /tmp /var/tmp /root /var/empty /lib/modules
      chmod 1777 /tmp /var/tmp
      chmod 700 /root
    '';
    enableFakechroot = true; # let npm-agent postinstall scripts run
  };
}
