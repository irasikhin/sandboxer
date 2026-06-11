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

        # OCI toolbox image: base userland + the agents + the sandboxer binary
        # (which also serves as the `_proxy` egress sidecar). Credentials are NOT
        # baked — they are bind-mounted at run time.
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
            ++ agentPkgs
            ++ [ pkgs.sandboxer ];
          config = {
            # No Entrypoint: `docker run img <cmd…>` runs <cmd> directly (the
            # launcher always passes a full command: bash -lc …, sandboxer _proxy …).
            Cmd = [
              "bash"
              "-l"
            ];
            WorkingDir = "/work";
            Env = [
              "PATH=/bin"
              "SSL_CERT_FILE=${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt"
              # Guard: inside the container only pull/push/show/list/diff are allowed.
              "SANDBOXER_IN_CONTAINER=1"
            ];
          };
          fakeRootCommands = ''
            mkdir -p /work /tmp /run/sandboxer
            chmod 1777 /tmp
          '';
          enableFakechroot = true; # let npm-agent postinstall scripts run
        };
      in
      {
        packages = {
          default = pkgs.sandboxer;
          sandboxer = pkgs.sandboxer;
          image = image;
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

          # Build the image and load it into host podman/docker. Does not touch
          # host systemd.
          build-image = {
            type = "app";
            meta.description = "Build the sandboxer toolbox image and load it into podman/docker";
            program = "${
              pkgs.writeShellApplication {
                name = "sandboxer-build-image";
                text = ''
                  img=$(nix build --no-link --print-out-paths "${self}#image")
                  if command -v podman >/dev/null 2>&1; then podman load < "$img";
                  elif command -v docker >/dev/null 2>&1; then docker load < "$img";
                  else echo "need podman or docker on the host" >&2; exit 1; fi
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
