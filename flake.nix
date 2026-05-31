{
  description = "sandboxer — config-driven, multi-agent, containerized dev sandboxes (Nix-профили, нативный /sandbox как опция)";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    # Набор популярных кодинг-агентов (claude-code, codex, opencode, gemini-cli, aider, pi, …),
    # обновляется ежедневно, со своим бинарным кэшем. Это рычаг «регулярно пересобирать образ».
    llm-agents.url = "github:numtide/llm-agents.nix";
    llm-agents.inputs.nixpkgs.follows = "nixpkgs";
  };

  outputs = { self, nixpkgs, llm-agents }:
    let
      # Linux-only: и нативный sandbox (bubblewrap), и контейнерный бэкенд (podman/docker).
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAll = f: nixpkgs.lib.genAttrs systems (system: f system nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAll (system: pkgs:
        let
          lib = pkgs.lib;

          # Зависимости в PATH обёртки. claude/podman/docker/systemd НЕ бандлятся — берутся с хоста
          # (через --prefix PATH сохраняем хостовый PATH). nix тоже с хоста (на NixOS он всегда есть).
          runtimeDeps = with pkgs; [
            bash coreutils gawk gnused gnugrep
            rsync git nodejs_22 jq bubblewrap socat
          ];

          sandboxer = pkgs.stdenvNoCC.mkDerivation {
            pname = "sandboxer";
            version = "0.2.0";
            src = ./.;
            nativeBuildInputs = [ pkgs.makeWrapper ];
            dontBuild = true;
            installPhase = ''
              runHook preInstall
              install -Dm755 bin/sandboxer                 "$out/bin/sandboxer"
              install -Dm755 libexec/sandboxer-cfg.mjs      "$out/libexec/sandboxer-cfg.mjs"
              install -Dm644 agents/registry.nix            "$out/share/sandboxer/registry.nix"
              install -Dm644 sandboxer.tasks.example         "$out/share/sandboxer/sandboxer.tasks.example"
              install -Dm644 examples/sandboxer.nix          "$out/share/sandboxer/examples/sandboxer.nix"
              install -Dm644 examples/with-deps.nix          "$out/share/sandboxer/examples/with-deps.nix"
              # --prefix: добавляем зависимости, сохраняя PATH хоста (claude/podman/nix остаются доступны).
              # --set: жёстко указываем пути к нашим хелперам, чтобы скрипт не угадывал их относительно $0.
              wrapProgram "$out/bin/sandboxer" \
                --prefix PATH : ${lib.makeBinPath runtimeDeps} \
                --set-default SANDBOXER_CFG      "$out/libexec/sandboxer-cfg.mjs" \
                --set-default SANDBOXER_REGISTRY "$out/share/sandboxer/registry.nix"
              runHook postInstall
            '';
            meta = with lib; {
              description = "Config-driven multi-agent dev sandboxes (Nix-профили, podman/native бэкенды)";
              mainProgram = "sandboxer";
              platforms = platforms.linux;
              license = licenses.mit;
            };
          };

          # Агенты для образа: берём .package из реестра (отсутствующие в llm-agents -> null -> отброшены).
          llmAgents = llm-agents.packages.${system};
          agentPkgs = lib.filter (p: p != null)
            (map (a: a.package) (lib.attrValues (import ./agents/registry.nix { inherit pkgs llmAgents; })));

          # OCI-образ-тулбокс: базовый userland + все агенты + сам sandboxer. Слоёный
          # (maxLayers), так что `nix flake update llm-agents` пересобирает только слои агентов.
          # Креды НЕ запекаются — биндятся при `podman run` (см. bin/sandboxer auth_flags).
          image = pkgs.dockerTools.buildLayeredImage {
            name = "sandboxer-toolbox";
            tag = "latest";
            maxLayers = 120;
            contents = (with pkgs; [
              bashInteractive coreutils git rsync nodejs_22 jq
              curl cacert gnused gawk gnugrep openssh which
            ]) ++ agentPkgs ++ [ sandboxer ];
            config = {
              Entrypoint = [ "/bin/bash" ];
              WorkingDir = "/work";
              Env = [
                "PATH=/bin"
                "SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt"
                "NODE_PATH=/lib/node_modules"
                "SANDBOXER_IN_CONTAINER=1"   # сторож: в контейнере разрешены только pull/push/show/list/diff
              ];
              # User задаётся при запуске (--user $(id -u):$(id -g)) для совпадения с хостом.
            };
            fakeRootCommands = ''
              mkdir -p /work /tmp /run/sandboxer
              chmod 1777 /tmp
            '';
            enableFakechroot = true;   # позволяет postinstall-скриптам npm-агентов отработать
          };
        in
        {
          default = sandboxer;
          sandboxer = sandboxer;
          image = image;
        });

      apps = forAll (system: pkgs:
        let self' = self.packages.${system}; in
        {
          default = { type = "app"; program = "${self'.default}/bin/sandboxer"; };
          sandboxer = { type = "app"; program = "${self'.default}/bin/sandboxer"; };

          # Собрать образ и загрузить его в podman/docker (хостовые). НЕ трогает host systemd.
          build-image = {
            type = "app";
            program = "${pkgs.writeShellApplication {
              name = "sandboxer-build-image";
              text = ''
                img=$(nix build --no-link --print-out-paths "${self}#image")
                if command -v podman >/dev/null 2>&1; then podman load < "$img";
                elif command -v docker >/dev/null 2>&1; then docker load < "$img";
                else echo "нужен podman или docker на хосте" >&2; exit 1; fi
              '';
            }}/bin/sandboxer-build-image";
          };
        });

      devShells = forAll (system: pkgs: {
        default = pkgs.mkShell {
          packages = [ self.packages.${system}.default pkgs.nodejs_22 pkgs.jq pkgs.podman ];
          shellHook = ''
            echo "sandboxer devShell — нужен установленный 'claude' (и podman/docker для контейнерного бэкенда)."
            echo "образ:  nix run .#build-image     профиль:  sandboxer create --config feat.nix"
          '';
        };
      });
    };
}
