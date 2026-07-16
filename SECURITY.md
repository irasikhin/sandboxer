# Security Policy

## Supported Versions

sandboxer is pre-1.0; the CLI flags and on-disk layout still change between minor
versions (the `sandboxer.nix` schema is evolving — this line of releases
removed several knobs; a config using a retired key fails with a migration hint).

While pre-1.0, **only the latest `0.x` release** receives security fixes — there
are no maintained back-ports to earlier `0.y` lines. A fix ships as the next
patch (or minor) release; upgrade to the newest tag to receive it.

| Version            | Supported          |
|--------------------|--------------------|
| latest `0.x`       | :white_check_mark: |
| any older release  | :x:                |

## Reporting a Vulnerability

Do not open a public issue for a vulnerability. Report security issues by
emailing the maintainer listed in the GitHub repository profile, or by using
GitHub private vulnerability reporting if it is enabled for this repository.

Please include:

- affected sandboxer version or commit;
- operating system and install method;
- a minimal reproduction that does not include real credentials or API keys.

This is a single-maintainer open-source project, so reports are handled on a
**best-effort** basis. Expect an initial acknowledgement within a few days
(typically under a week); a fix then ships in the next release once confirmed.

## Threat model

sandboxer is a workflow tool for a **single developer on their own machine**:
each developer runs coding agents locally, with their own key or subscription.
The adversary it defends against is **the agent on the owner's laptop** — not one
developer's agent against another's. So the goal is to keep a misbehaving or
prompt-injected agent from reaching the developer's host, credentials and other
projects. It is **not** a hardened multi-tenant isolation layer; do not run an
agent you actively distrust and assume it is contained.

The rest of this section is what the isolation actually gives you, and — just as
important — where it stops.

### What the isolation gives you

- **Sources are sparse git worktrees; git never enters the container.** Each
  source repo is checked out host-side into a worktree on its configured
  branch, narrowed by the profile's include patterns — the container
  is bind-mounted ONLY those (sparse) contents. No git metadata is mounted, so
  the agent cannot read repo history, widen the selection, touch refs, or
  reach hooks/config at all; you review and commit its file edits on the host
  (`git log`/`git diff`/`git merge <branch>`). There is no copy-back
  over host files.

- **Isolated `$HOME` — host configs only by opt-in, and only as a copy.** Each
  sandbox has its own private home (`_home/<slug>` under the XDG state dir,
  `0700`), mounted as `$HOME`. The host's real agent config — `~/.claude`,
  `~/.claude.json`, tokens, project history, MCP servers — and your `~/.ssh`,
  `~/.aws`, etc. are **never mounted** in, and API-key env vars are never
  passed through. A profile may set `hostConfigs = true` (the scaffolded
  config does) to **seed** the sandbox home with a one-time COPY of the
  agents' own configs (credentials included, bulky/private transcripts
  excluded), so agents start authenticated. The trade is explicit: code
  running in that sandbox can read those copied credentials, and its egress
  allowlist is the wall between them and an exfiltration attempt — but it
  still cannot touch the host's real config (a hook written into the
  sandbox's settings.json never executes on the host), copies are per-sandbox
  (no cross-sandbox races), and an in-sandbox login/logout is never
  overwritten by a later seed. Keeping `hostConfigs` off returns the old
  posture: log in or export a key inside the sandbox when a task needs it.

- **Clean container environment.** The agent runs in a podman/docker container
  from a clean, explicit environment: it does **not** inherit your host shell, so
  an `AWS_*` / `GITHUB_*` / `*_TOKEN` left in your environment is invisible to it
  unless you wire it in.

- **Unprivileged container + resource caps.** The backend runs the agent
  `--user` (non-root), `--cap-drop=ALL`, `--security-opt no-new-privileges`, and
  applies the profile's `limits:` (`--memory` / `--cpus` / `--pids-limit`) when
  set. This reduces, but does not eliminate, the blast radius. A profile that
  sets `nestedContainers = true` keeps all of the above but gives up the syscall
  filter — see below.

- **Egress allowlist.** The agent runs on an `--internal` network whose sole exit
  is a **squid** forward-proxy sidecar that permits only `egress.allowedDomains`
  (everything else → 403). It **fails closed**: if the allowlist is required but
  the proxy cannot start (or no domains are allowed) the run is refused, never
  silently opened. Disable deliberately with `egress.enabled = false` /
  `SANDBOXER_NO_EGRESS=1`.

### Where it stops (know these before you trust it)

- **`nestedContainers = true` turns off the sandbox's syscall filter.** The
  toolbox image ships a rootless podman, but a container cannot create the user
  namespace podman re-execs into while the engine's default seccomp profile is
  active — that profile denies `clone(CLONE_NEWUSER)` to anything without
  `CAP_SYS_ADMIN`. Opting in therefore passes `seccomp=unconfined` and
  `systempaths=unconfined` (the latter unmasks `/proc`, which the nested
  container's own `procfs` mount needs), plus `/dev/net/tun` and `/dev/fuse`.
  What it does **not** do is hand over privilege: no `--privileged`, no
  `--cap-add`, and `no-new-privileges` stays on, so the nested podman gets no
  subordinate uid range and runs single-uid. Net effect: an escape no longer has
  to get past a syscall allowlist. Leave it off unless the sandbox actually
  builds or runs containers.

- **The worktree's `.git` pointer file is writable.** Git metadata is not
  mounted (the whole hooks/config/object-store attack surface of earlier
  releases is gone with it), but a managed worktree's root still contains the
  one-line `.git` *pointer file*, and the mount is rw. Corrupting or deleting
  it cannot execute anything — the path it names does not exist in the
  container — but it can confuse the host-side worktree until you run
  `git worktree repair` (or `sandboxer recreate`). Also remember an **adopted**
  source (`srcs branch:` naming your existing checkout) is your live tree,
  mounted rw by explicit request — there is no isolation from it.

- **The config is code.** `sandboxer.nix` is EVALUATED on the host — under a
  restricted eval (no network access, imports/reads only inside the config's
  own directory), so it cannot exfiltrate or fetch, but it is still a program.
  Beyond that, `setup` is a shell script run in the sandbox and the image hook
  (`image.hook`/`image.nix`) executes nix code at image-build time inside the
  builder container. A committed `sandboxer.nix` therefore carries
  **executable content** on three levels — treat it as code and read an
  untrusted repo's config before `create`/`enter`.

- **`extraMounts` are read-write by default.** An `extraMounts` entry grants the
  agent access to that host path — **rw unless you set `mode: ro`**. Scope them
  narrowly; a broad rw mount hands the agent a hole straight to the host FS.

- **The allowlist is domain-level, not a guarantee.** Even when active it is a
  best-effort guardrail: domain-fronting, abuse of an already-allowed domain
  (e.g. exfiltration through a permitted API or git host), and DNS tricks can
  defeat it. `egress.routes` and `egress.proxy` change *where* allowed traffic
  goes, not *what* is allowed.

  > **`egress.enabled = false` replaces the allowlist.** An `egress.proxy` URL
  > with `egress.enabled = false` makes sandboxer treat that proxy as the
  > boundary: it does **not** start the allowlist sidecar, so egress is governed
  > by your proxy's policy, not by `egress.allowedDomains`. With the allowlist on
  > (the default) the proxy is chained *through* the allowlist, which keeps
  > applying — don't run `egress.enabled = false` + proxy expecting the domain
  > allowlist to also apply.

- **Not a multi-tenant boundary.** Assume an adversarial agent can reach anything
  the sandbox can reach (the allowed domains, the mounted paths). sandboxer
  raises the cost of a bad agent on your own machine; it is not a
  container-escape-proof jail.
