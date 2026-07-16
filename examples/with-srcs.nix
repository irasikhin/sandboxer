# Config that narrows the sandbox to a subset of the repo, with a container
# backend and a custom agent set.
#
# A sandbox exposes SOURCES: each srcs entry is a git repo checked out into a
# host-side worktree; ONLY the files its gitignore-style include patterns
# select are visible inside the container — git itself never is. Work returns
# as an ordinary branch (feat/<name>) you review and commit on the HOST;
# there is no copy-in and no push-back.
{
  name = "integ";
  backend = "podman";

  egress = {
    allowedDomains = [
      "api.openai.com" "api.anthropic.com" "registry.npmjs.org" "pypi.org"
      "repo.maven.apache.org" "repo1.maven.org" "central.sonatype.com"
    ];
  };

  # Srcs: the repos (and slices of them) the sandbox sees — ALWAYS explicit,
  # there is no implicit default. src is a path to a repo root (relative paths
  # resolve against the project root), include narrows it with gitignore-style
  # patterns (omit include for the whole repo; branch adopts an existing
  # branch/worktree).
  srcs = [
    {
      src = ".";
      include = [ "/src/proto/" "/shared/lib/" ];
    }
    # { src = "../other-repo"; }                       # a second repo, whole
    # { src = "../protolib"; branch = "feat/proto-v2"; } # adopt an existing branch/worktree
  ];

  # Extra bind mounts and env (extraMounts is the way to bring in non-git
  # trees). Example: persist Maven's local repo across runs — the sandbox
  # $HOME is ephemeral, so bind the host ~/.m2 at a fixed path and point
  # Maven at it:
  # extraMounts = [ { source = "/home/me/.m2"; target = "/opt/.m2"; mode = "rw"; } ];
  # env = {
  #   MAVEN_OPTS = "-Dmaven.repo.local=/opt/.m2/repository";
  #   MAVEN_ARGS = "-s /opt/.m2/settings.xml";
  # };
}
