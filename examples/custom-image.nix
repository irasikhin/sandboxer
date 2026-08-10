# A config with a customized toolbox image (built with host nix on first
# use):
#
#   sandboxer image build -f ./examples/custom-image.nix   # build the variant
#   sandboxer create -f ./examples/custom-image.nix        # or just create +
#   sandboxer enter  custom-image                          # enter — auto-builds
#
# The customization is content-addressed: this profile's sandboxes run
# sandboxer-toolbox:var-<12hex> (hashed over the input pins, packages, files,
# env and the overlay's content), built on first use and shared by identical
# profiles. The stock sandboxer-toolbox:latest is untouched.
{
  name = "custom-image";
  backend = "microsandbox";
  srcs = [ { src = "."; branch = "feat/custom-image"; } ];

  image = {
    # nixpkgs attr names baked into the image (dotted paths allowed). Attrs
    # defined by the overlay below are listed here like any other.
    packages = [ "httpie" "greet" ];

    # Static text files baked at absolute paths (shell drop-ins under
    # /etc/sandboxer/rc.d/*.sh are sourced by every interactive shell).
    files."/etc/sandboxer/rc.d/10-custom.sh" = "alias hi=greet";

    # Static additions to the image's OCI env.
    env.SANDBOX_FLAVOR = "custom";

    # Anything that needs pkgs at build time is a PLAIN nixpkgs overlay in
    # its own file — see custom-image-overlay.nix.
    overlay = "./custom-image-overlay.nix";

    # Flake-input pins. By default both inputs TRACK the remote heads — every
    # `sandboxer image build` re-resolves them, so agents auto-update. Set a
    # full 40-hex commit to hold one still.
    # llmAgentsRev = "<full 40-hex commit>";
    # nixpkgsRev = "<full 40-hex commit>";
  };
}
