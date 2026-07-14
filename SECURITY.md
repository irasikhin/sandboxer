# Security Policy

## Supported Versions

sandboxer is pre-1.0; the CLI flags and on-disk layout still change between minor
versions (the `.sandboxer/config.yaml` schema is evolving — this line of releases
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

- **Git-worktree working copy.** A sandbox is a `git worktree` of your repo on
  branch `feat/<slug>-sb`. The agent commits there; its work returns as an
  ordinary branch you review (`git log`/`git diff`/`git merge feat/<slug>-sb`)
  before it touches your main line. There is no copy-back over host files.

- **Isolated `$HOME` — no host credentials.** Each sandbox has its own private
  home (`_home/<slug>` under the XDG state dir, `0700`), mounted as `$HOME`. The
  host's real agent config — `~/.claude`, `~/.claude.json`, tokens, project
  history, MCP servers — and your `~/.ssh`, `~/.aws`, etc. are **never** mounted
  in. The **recommended, safe auth path is to log in inside the sandbox** (e.g.
  `claude login`): the token lands in that sandbox's home and never reaches the
  host. API-key env vars (e.g. `ANTHROPIC_API_KEY`) are passed through only when
  you have set them on the host — an explicit opt-in. Wire in only the agents and
  keys the task needs.

- **Clean container environment.** The agent runs in a podman/docker container
  from a clean, explicit environment: it does **not** inherit your host shell, so
  an `AWS_*` / `GITHUB_*` / `*_TOKEN` left in your environment is invisible to it
  unless you wire it in.

- **Unprivileged container + resource caps.** The backend runs the agent
  `--user` (non-root), `--cap-drop=ALL`, `--security-opt no-new-privileges`, and
  applies the profile's `limits:` (`--memory` / `--cpus` / `--pids-limit`) when
  set. This reduces, but does not eliminate, the blast radius.

- **Egress allowlist.** The agent runs on an `--internal` network whose sole exit
  is a **squid** forward-proxy sidecar that permits only `network.allowedDomains`
  (everything else → 403). It **fails closed**: if the allowlist is required but
  the proxy cannot start (or no domains are allowed) the run is refused, never
  silently opened. Disable deliberately with `egress: false` /
  `SANDBOXER_NO_EGRESS=1`.

### Where it stops (know these before you trust it)

- **The shared git object store is writable.** For git to work in-container the
  repo's common git dir is bind-mounted. PR-hardening masks it read-only and
  restores rw only to `objects/`, `refs/`, `logs/`, `worktrees/`, and neutralizes
  `core.hooksPath` / `core.fsmonitor` via `GIT_CONFIG_*` — so an agent can **no
  longer** get host code execution by writing `.git/hooks/*` or `.git/config`.
  Residual risk remains: the agent can still **rewrite refs** (e.g. move
  `refs/heads/main`) in the shared object store, so do not blindly trust your
  repo's branches after a sandbox run — review the sandbox branch and be wary of
  unexpected movement elsewhere. And **submodule git dirs under `.git/modules/`
  are not masked**, so a repo with submodules still has a hooks/config surface
  there. Prefer not to run untrusted agents against a submodule-heavy repo.

- **`setup:` and `image.nix` run arbitrary code.** A profile's `setup:` is a
  shell script run in the sandbox, and `image.nix` executes nix code at
  image-build time. A committed `.sandboxer/config.yaml` therefore carries
  **executable content** — treat it as code. Do not `create`/`enter` an untrusted
  repo's sandbox without reading its `.sandboxer/` first; `image.nix` runs on the
  host's build engine.

- **`extraMounts` are read-write by default.** An `extraMounts` entry grants the
  agent access to that host path — **rw unless you set `mode: ro`**. Scope them
  narrowly; a broad rw mount hands the agent a hole straight to the host FS.

- **The allowlist is domain-level, not a guarantee.** Even when active it is a
  best-effort guardrail: domain-fronting, abuse of an already-allowed domain
  (e.g. exfiltration through a permitted API or git host), and DNS tricks can
  defeat it. `network.routes` and `network.proxy` change *where* allowed traffic
  goes, not *what* is allowed.

  > **`egress: false` replaces the allowlist.** A `network.proxy` URL with
  > `egress: false` makes sandboxer treat that proxy as the boundary: it does
  > **not** start the allowlist sidecar, so egress is governed by your proxy's
  > policy, not by `network.allowedDomains`. With egress **on** (the default) the
  > proxy is chained *through* the allowlist, which keeps applying — don't run
  > `egress: false` + proxy expecting the domain allowlist to also apply.

- **Not a multi-tenant boundary.** Assume an adversarial agent can reach anything
  the sandbox can reach (the allowed domains, the mounted paths, the shared git
  object store). sandboxer raises the cost of a bad agent on your own machine; it
  is not a container-escape-proof jail.
