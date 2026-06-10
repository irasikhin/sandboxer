# sandbox-image.nix — the user hook a profile's `image.nix` points at, imported
# by the embedded toolbox flake during `sandboxer build-image` (or the
# auto-build on first enter). A function over { pkgs } returning any of FOUR
# keys: packages, files, env, overlay. The contract is fail-closed — an unknown
# key aborts the build, so a typo never silently drops a customization.
#
# Two-phase evaluation: the function is first called with BASE nixpkgs and only
# `overlay` is read; nixpkgs is then re-imported with the overlay applied, and
# the function is called again with that final `pkgs` for packages/files/env.
# So `packages` below already sees the overlay — but the overlay itself cannot
# reference its own result (no recursive overlays).
{ pkgs }:
{
  # nixpkgs overlay (phase 1): add or override packages; everything in phase 2
  # sees the result.
  overlay = final: prev: {
    hello-sandboxer = prev.writeShellScriptBin "hello-sandboxer" ''
      echo "hello from a custom toolbox image"
    '';
  };

  # Extra store paths baked into the image — `pkgs` has the overlay applied,
  # so hello-sandboxer (defined above) resolves here.
  packages = [
    pkgs.hello-sandboxer
    pkgs.httpie
  ];

  # Text files at absolute paths inside the image. /etc/sandboxer/rc.d/*.sh
  # are the shell's plugin drop-ins, sourced by every interactive shell.
  files = {
    "/etc/sandboxer/rc.d/10-custom.sh" = ''
      alias hi=hello-sandboxer
    '';
  };

  # Appended to the image's OCI env; a user variable overrides a same-named
  # default (last occurrence wins).
  env = {
    SANDBOX_FLAVOR = "custom";
  };
}
