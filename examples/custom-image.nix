# A config with a customized toolbox image (no nix builds on your machine —
# the builder container does everything; only config EVALUATION uses host nix):
#
#   sandboxer image build -f ./examples/custom-image.nix   # build the variant
#   sandboxer create -f ./examples/custom-image.nix        # or just create +
#   sandboxer enter  custom-image                          # enter — auto-builds
#
# The customization is content-addressed: this profile's sandboxes run
# sandboxer-toolbox:var-<12hex> (hashed over the input pins, the package set
# and the hook's content), built on first use and shared by identical
# profiles. The stock sandboxer-toolbox:latest is untouched.
{
  name = "custom-image";
  backend = "podman";
  srcs = [ { src = "."; } ];

  image = {
    # Extra nixpkgs attrs baked into the image (dotted paths allowed).
    extraPkgs = [ "httpie" ];

    # The image hook INLINE — nix source of a { pkgs }: { packages, files,
    # env, overlay } function (fail-closed: an unknown key aborts the build).
    # Two-phase evaluation: `overlay` is read first, nixpkgs is re-imported
    # with it applied, then the function is called again for the rest.
    hook = ''
      { pkgs }:
      {
        # overlay = final: prev: {
        #   hello-sandboxer = prev.writeShellScriptBin "hello-sandboxer" "echo hi";
        # };
        # packages = [ pkgs.ripgrep ];
        # files."/etc/sandboxer/rc.d/10-custom.sh" = "alias hi=hello-sandboxer";
        # env = { SANDBOX_FLAVOR = "custom"; };
      }
    '';

    # Or point at a separate file instead of inlining:
    # nix = "./my-image-hook.nix";
  };
}
