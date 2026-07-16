# A config with a customized toolbox image (builds run in a builder
# container; host nix only evaluates this config):
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
  backend = "podman";
  srcs = [ { src = "."; } ];

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

    # Flake-input pin overrides: "latest" or a full 40-hex commit.
    # llmAgentsRev = "latest";
    # nixpkgsRev = "<full 40-hex commit>";
  };
}
