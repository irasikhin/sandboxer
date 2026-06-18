{
  description = "sandboxer — config-driven, multi-agent, containerized dev sandboxes";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
    # Catalog of coding agents (claude-code, codex, opencode, crush, gemini-cli,
    # aider, pi, …), refreshed daily with its own binary cache — the lever for
    # rebuilding the toolbox image regularly.
    llm-agents.url = "github:numtide/llm-agents.nix";
    llm-agents.inputs.nixpkgs.follows = "nixpkgs";
  };

  outputs =
    {
      self,
      nixpkgs,
      flake-utils,
      llm-agents,
    }:
    let
      overlay = final: prev: {
        sandboxer = final.callPackage ./nix/package.nix { };
      };
    in
    {
      overlays.default = overlay;
    }
    // flake-utils.lib.eachSystem [ "x86_64-linux" "aarch64-linux" ] (
      system:
      let
        pkgs = import nixpkgs {
          inherit system;
          overlays = [ overlay ];
        };
        lib = pkgs.lib;
        llmAgents = llm-agents.packages.${system};

        # Agents baked into the toolbox image, derived from the SAME source of
        # truth the Go binary embeds (internal/registry/registry.json): take each
        # agent's nixPackage from llm-agents, skip those with "image": false
        # (codex — Rust, dominates build time) and any missing for this platform.
        registry = builtins.fromJSON (builtins.readFile ./internal/registry/registry.json);
        imageAgentNames = lib.filter (n: (registry.${n}.image or true) != false) (
          builtins.attrNames registry
        );
        agentPkgs = lib.filter (p: p != null) (
          map (n: llmAgents.${registry.${n}.nixPackage or n} or null) imageAgentNames
        );

        # OCI toolbox image: base userland + the agents. The sandboxer binary is
        # deliberately NOT baked in — it is a HOST tool, never reachable from
        # inside the sandbox. Egress is enforced by a separate squid sidecar
        # (proxyImage), not by the binary. Credentials are NOT baked — they are
        # bind-mounted at run time.
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
            ])
            ++ agentPkgs;
          config = {
            # No Entrypoint: `docker run img <cmd…>` runs <cmd> directly (the
            # launcher always passes a full command, e.g. bash -lc …).
            Cmd = [
              "bash"
              "-l"
            ];
            WorkingDir = "/work";
            Env = [
              "PATH=/bin"
              "SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt"
            ];
          };
          fakeRootCommands = ''
            mkdir -p /work /tmp
            chmod 1777 /tmp
          '';
          enableFakechroot = true; # let npm-agent postinstall scripts run
        };

        # Egress proxy image: a minimal squid that enforces the domain allowlist
        # for a sandbox. It runs our generated /etc/sandboxer/squid.conf (bind-
        # mounted at run time by the egress package) as the unprivileged run user;
        # the config logs to std streams and keeps no on-disk cache, so no
        # writable image dirs are needed. No sandboxer binary anywhere in the
        # network path.
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
      in
      {
        packages = {
          default = pkgs.sandboxer;
          sandboxer = pkgs.sandboxer;
          image = image;
          proxyImage = proxyImage;
        };

        apps = {
          default = {
            type = "app";
            program = "${pkgs.sandboxer}/bin/sandboxer";
          };
          sandboxer = {
            type = "app";
            program = "${pkgs.sandboxer}/bin/sandboxer";
          };

          # Build the toolbox + egress-proxy images and load them into host
          # podman/docker. Does not touch host systemd.
          build-image = {
            type = "app";
            meta.description = "Build the sandboxer toolbox + proxy images and load them into podman/docker";
            program = "${
              pkgs.writeShellApplication {
                name = "sandboxer-build-image";
                text = ''
                  load() {
                    if command -v podman >/dev/null 2>&1; then podman load < "$1";
                    elif command -v docker >/dev/null 2>&1; then docker load < "$1";
                    else echo "need podman or docker on the host" >&2; exit 1; fi
                  }
                  img=$(nix build --no-link --print-out-paths "${self}#image")
                  load "$img"
                  prox=$(nix build --no-link --print-out-paths "${self}#proxyImage")
                  load "$prox"
                '';
              }
            }/bin/sandboxer-build-image";
          };
        };

        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gopls
            gotools
            delve
            golangci-lint
            nixfmt
            # runtime deps for exercising the CLI locally
            git
            rsync
            bubblewrap
            jq
            podman
          ];
          shellHook = ''
            echo "sandboxer devShell — go toolchain + linters."
            echo "build:  go build ./cmd/sandboxer   image:  nix run .#build-image"
          '';
        };

        checks.default = pkgs.sandboxer;

        formatter = pkgs.nixfmt;
      }
    );
}
