# Several environments in one file (the profiles form) — reuse is ordinary
# nix: a let-bound base attrset merged into each section with //. The section
# attrs win over the merged base (rightmost // operand). `default` names the
# profile used when you don't name one.
#
# Pick a profile by its section name:
#   sandboxer create web        # the `web` profile
#   sandboxer create api-prod   # api base + production overrides
#   sandboxer create            # the `default` profile (web)
#
# (The flat one-profile-per-file form still works — see config.nix.)
let
  net = {
    allowedDomains = [
      "api.anthropic.com" "github.com" "registry.npmjs.org"
      "repo.maven.apache.org" "repo1.maven.org" "central.sonatype.com"
    ];
  };
  api = {
    backend = "docker";
    egress = net;
    srcs = [ { src = "."; include = [ "/shared/proto/" ]; } ];
    env.NODE_ENV = "development";
  };
in
{
  profiles = {
    # Frontend: container backend, sandbox narrowed to the shared UI lib.
    web = {
      backend = "podman";
      egress = net;
      srcs = [ { src = "."; include = [ "/shared/ui/" ]; } ];
      env.NODE_ENV = "development";
    };

    # Backend API base.
    inherit api;

    # Production variant: the api base with only env overridden.
    api-prod = api // {
      env.NODE_ENV = "production";
    };
  };

  default = "web";
}
