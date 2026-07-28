{
  description = "sandboxer — config-driven, multi-agent, containerized dev sandboxes";

  # llm-agents' binary cache, restated here because nix only honors the
  # nixConfig of the top-level flake — an input's nixConfig is ignored. Without
  # it, `nix run .#build-image` compiles every agent from source (gemini-cli's
  # large npm-deps fetch can then OOM the builder). Keep in sync with the
  # embedded toolbox flake (internal/toolbox/assets/flake.nix) and llm-agents.
  nixConfig = {
    extra-substituters = [ "https://cache.numtide.com" ];
    extra-trusted-public-keys = [
      "niks3.numtide.com-1:DTx8wZduET09hRmMtKdQDxNNthLQETkc/yaX7M4qK0g="
    ];
  };

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
        # The microVM runtime for `backend = "microvm"` (repackaged upstream
        # binary — not in nixpkgs, needs patching to run on NixOS).
        smolvm = final.callPackage ./nix/smolvm.nix { };
        # The second microVM runtime, for `backend = "microsandbox"` (same
        # libkrun VMM, a richer CLI — see docs/microsandbox-spike.md).
        microsandbox = final.callPackage ./nix/microsandbox.nix { };
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

        # The toolbox + egress-proxy images. Their CONTENTS are defined once, in
        # internal/toolbox/assets/images.nix, and shared with the flake embedded
        # in the binary — the one `sandboxer image build` runs. Keeping a second
        # copy here is exactly how `.#image` silently rotted into a userland that
        # no user ever gets (no podman, no runtimes, no shell rc) while the e2e
        # suite kept testing it. This build has no profile, so it passes only the
        # agents; the sandboxer binary is deliberately NOT baked in (a HOST tool,
        # never reachable from inside), and credentials are bind-mounted at run
        # time, never baked.
        images = import ./internal/toolbox/assets/images.nix {
          inherit pkgs agentPkgs;
        };

        # A tiny busybox image for the integration suite's smoke tier, sourced
        # from the nix cache (cache.nixos.org) instead of Docker Hub. The homelab
        # CI cluster's egress is RU-degraded and 503s on registry-1.docker.io, so
        # `docker pull alpine` there is unreliable; nix builds go through the
        # trusted binary caches that already back the toolbox build. Tagged
        # alpine:latest because internal/itest/engine.go looks for an
        # already-present alpine/busybox smoke image (busybox is an accepted one).
        smokeImage = pkgs.dockerTools.buildLayeredImage {
          name = "alpine";
          tag = "latest";
          contents = [ pkgs.busybox ];
          config = {
            Cmd = [ "/bin/sh" ];
            WorkingDir = "/tmp";
          };
          fakeRootCommands = ''
            mkdir -p /tmp
            chmod 1777 /tmp
          '';
          enableFakechroot = true;
        };
      in
      {
        packages = {
          default = pkgs.sandboxer;
          sandboxer = pkgs.sandboxer;
          smolvm = pkgs.smolvm;
          microsandbox = pkgs.microsandbox;
          inherit (images) image proxyImage;
          smokeImage = smokeImage;
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

          # The microVM runtime, so `nix run .#smolvm -- machine ls` works
          # without a system install (and `sandboxer --backend microvm` can point
          # SANDBOXER_SMOLVM at the built path).
          smolvm = {
            type = "app";
            meta.description = "smolvm microVM runtime (sandboxer's microvm backend)";
            program = "${pkgs.smolvm}/bin/smolvm";
          };

          # The second microVM runtime, so `nix run .#microsandbox -- list`
          # works without a system install (and `sandboxer --backend
          # microsandbox` can point SANDBOXER_MSB at the built path).
          microsandbox = {
            type = "app";
            meta.description = "microsandbox microVM runtime (sandboxer's microsandbox backend)";
            program = "${pkgs.microsandbox}/bin/msb";
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
            smolvm # microvm backend runtime (SANDBOXER_SMOLVM picks it up)
            microsandbox # microsandbox backend runtime (SANDBOXER_MSB picks it up)
          ];
          shellHook = ''
            echo "sandboxer devShell — go toolchain + linters."
            echo "build:  go build ./cmd/sandboxer   image:  nix run .#build-image"
            echo "microvm: smolvm on PATH (backend = \"microvm\"), msb on PATH (backend = \"microsandbox\")"
          '';
        };

        checks.default = pkgs.sandboxer;

        formatter = pkgs.nixfmt;
      }
    );
}
