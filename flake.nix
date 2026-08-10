{
  description = "sandboxer — config-driven, multi-agent, containerized dev sandboxes";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs =
    {
      self,
      nixpkgs,
      flake-utils,
    }:
    let
      overlay = final: prev: {
        sandboxer = final.callPackage ./nix/package.nix { };
        # The microVM runtime for `backend = "microsandbox"` (libkrun;
        # repackaged upstream binary — not in nixpkgs, needs patching to run
        # on NixOS. See docs/microsandbox-spike.md).
        microsandbox = final.callPackage ./nix/microsandbox.nix { };
        # The one image agent nixpkgs does not carry — vendored beside the
        # embedded flake, which grafts it the same way (single source).
        pi = final.callPackage ./internal/toolbox/assets/pi/package.nix { };
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
          # The toolbox image bakes agents that are unfree in nixpkgs
          # (claude-code); mirror the embedded flake's stance.
          config.allowUnfree = true;
          overlays = [ overlay ];
        };
        lib = pkgs.lib;

        # Agents baked into the toolbox image, derived from the SAME source of
        # truth the Go binary embeds (internal/registry/registry.json): each
        # agent's nixPackage is a plain nixpkgs attr (prebuilt on
        # cache.nixos.org; pi comes via the overlay above), skipping agents
        # with "image": false (codex). Fail-closed on a vanished attr — an
        # image quietly missing an agent is worse than a broken build.
        registry = builtins.fromJSON (builtins.readFile ./internal/registry/registry.json);
        imageAgentNames = lib.filter (n: (registry.${n}.image or true) != false) (
          builtins.attrNames registry
        );
        agentPkgs = map (
          n:
          let
            attr = registry.${n}.nixPackage or n;
          in
          pkgs.${attr} or (throw "sandboxer: unknown agent package '${attr}' (registry.json nixPackage)")
        ) imageAgentNames;

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
          microsandbox = pkgs.microsandbox;
          inherit (images) image;
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

          # The microVM runtime, so `nix run .#microsandbox -- list` works
          # without a system install (and sandboxer can point SANDBOXER_MSB at
          # the built path).
          microsandbox = {
            type = "app";
            meta.description = "microsandbox microVM runtime (sandboxer's microsandbox backend)";
            program = "${pkgs.microsandbox}/bin/msb";
          };

          # Build the toolbox image: place the toolbox tar into the microVM
          # store (msb imports it from there on first use). Also loads it
          # into host podman/docker when one is present — convenient for
          # inspecting the image, though no backend runs it there anymore.
          # Does not touch host systemd.
          build-image = {
            type = "app";
            meta.description = "Build the sandboxer toolbox image into the microVM store (and podman/docker when present)";
            program = "${
              pkgs.writeShellApplication {
                name = "sandboxer-build-image";
                text = ''
                  load() {
                    if command -v podman >/dev/null 2>&1; then podman load < "$1";
                    elif command -v docker >/dev/null 2>&1; then docker load < "$1";
                    else echo "no docker/podman on the host — image placed in the microVM store only" >&2; fi
                  }
                  # The microVM store root: SANDBOXER_STATE, else
                  # $XDG_STATE_HOME/sandboxer, else ~/.local/state/sandboxer —
                  # mirroring config.StateRoot. The tar name is the default image
                  # reference with ':' mapped to '-' (sanitizeContainerName).
                  store_root="''${SANDBOXER_STATE:-''${XDG_STATE_HOME:-$HOME/.local/state}/sandboxer}"
                  store_dir="$store_root/images"
                  mkdir -p "$store_dir"
                  img=$(nix build --no-link --print-out-paths "''${self}#image")
                  tar="$store_dir/sandboxer-toolbox-latest.tar"
                  cp -L "$img" "$tar"
                  sha256sum -b "$tar" | cut -d' ' -f1 | tr -d '\n' > "$tar.sha256"
                  load "$img"
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
            microsandbox # microsandbox backend runtime (SANDBOXER_MSB picks it up)
          ];
          shellHook = ''
            echo "sandboxer devShell — go toolchain + linters."
            echo "build:  go build ./cmd/sandboxer   image:  nix run .#build-image"
            echo "microvm: msb on PATH (backend = \"microsandbox\")"
          '';
        };

        checks.default = pkgs.sandboxer;

        formatter = pkgs.nixfmt;
      }
    );
}
