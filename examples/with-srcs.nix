# Config that narrows the sandbox to a subset of the repo, with a container
# backend and a custom agent set.
#
# A sandbox exposes SOURCES: each srcs entry is a git repo checked out into a
# host-side worktree; ONLY the directories its include list names are visible
# inside the container — git itself never is. The worktree on the HOST stays a
# COMPLETE checkout (open it in your IDE): include narrows what gets mounted
# into the container, not what is on disk. Work returns as an ordinary branch
# (the one you configured) you review and commit on the HOST; there is no
# copy-in and no push-back.
{
  name = "integ";
  backend = "podman";

  egress = {
    allowedDomains = [
      "api.openai.com"
      "api.anthropic.com"
      "registry.npmjs.org"
      "pypi.org"
      "repo.maven.apache.org"
      "repo1.maven.org"
      "central.sonatype.com"
    ];
  };

  # Srcs: the repos (and slices of them) the sandbox sees — ALWAYS explicit,
  # there is no implicit default. src is a path to a repo root (relative paths
  # resolve against the project root); branch is REQUIRED — it names the
  # worktree's branch AND its directory (./sandboxes/<name>/<repo>/<branch>);
  # include lists the DIRECTORIES the container may see — anchored at the repo
  # root, trailing slash optional (omit include entirely for the whole repo).
  # Ant-style directory patterns work too: "/services/*/" (direct children),
  # "**/proto/" (a proto/ dir at ANY depth — a whole "**" segment recurses).
  # Patterns select directories, never files; one that matches nothing is an
  # error. Negations ("!/vendor/") and unanchored paths are refused: a mount
  # names a path, not a file set.
  # A branch already checked out somewhere is adopted as-is.
  srcs = [
    {
      src = ".";
      branch = "devops/integ";
      include = [
        "/src/proto/"
        "/shared/lib/"
        "**/generated/"
      ];
    }
    # { src = "../other-repo"; branch = "devops/integ"; }  # a second repo, whole
    # { src = "https://github.com/org/proto"; branch = "main"; include = [ "/proto/" ]; } # remote → cloned
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
