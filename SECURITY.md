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

### The isolation boundary is a hypervisor

Every sandbox is a real **microVM** on [microsandbox](https://microsandbox.dev)
(`msb`, libkrun — KVM on Linux, Hypervisor.framework on macOS). The agent runs
under its **own guest kernel**; the wall between it and the host is hardware
virtualization, not a shared-kernel namespace. There is no `--cap-drop`, no
seccomp profile and no user-namespace mapping to get right — an escape must
break the hypervisor, not win a syscall-filter argument.

Two consequences worth stating plainly:

- **The guest runs as uid 0, and that is fine.** Root inside the VM is root
  over the VM's own kernel and nothing else; the VM boundary is the privilege
  boundary, not the uid. Files the guest writes into a shared directory land on
  the host owned by **the invoking host user** (virtio-fs maps the identity),
  so guest-root never mints host-root files.
- **Nested containers work natively.** docker/podman inside the sandbox run
  against the guest's own kernel with a full uid range — no opt-in, no widened
  syscall filter, no capability grant on the host side. Their blast radius is
  the VM's.

Host requirements for that boundary: **nix** (a hard requirement of the CLI —
it evaluates `sandboxer.nix` and builds the toolbox image), the **msb** binary,
and **`/dev/kvm`** on Linux (nested KVM inside WSL2 on Windows). `sandboxer
doctor` checks all three. macOS (Apple Silicon / Hypervisor.framework) and
Windows/WSL2 are **cross-platform in code but not live-verified** — see
"Platform status" below.

### What the isolation gives you

- **Sources are git worktrees; git does not enter the sandbox unless a source
  asks it to.** Each source repo is checked out host-side into a complete
  worktree on its configured branch. The guest is shared ONLY the directories
  the profile's `include` lists (all of it when `include` is absent) — **the
  mount set is the boundary**: an excluded path is not shared, so it does not
  exist inside the sandbox, and the agent cannot reach it by any path. By
  default no git metadata is shared either, so the agent cannot read repo
  history, widen the selection, touch refs, or reach hooks/config at all; you
  review and commit its file edits on the host (`git log`/`git diff`/`git merge
  <branch>`). There is no copy-back over host files.

  The image's `git` carries a guard for exactly this shape: a worktree's `.git`
  is a pointer file naming an unmounted host gitdir, and plain git greets that
  with `fatal: not a git repository` — which an agent has been observed to
  "repair" with `git init`, orphaning the host worktree. The wrapper explains
  the design and refuses (exit 128) instead; git anywhere else in the guest
  works normally.

- **The opt-in git share (`git = "ro"` / `"rw"` on a source) is a deliberate
  hole in that wall.** It shares the repository's common git dir at its own host
  path — which is what makes the worktree's `.git` pointer resolve inside the
  guest — and it hands over the WHOLE repository, not the sandbox's branch:

  - **Every branch's history becomes readable**, including files a narrowing
    `include` would have withheld (`git show HEAD:excluded/path`), and any
    secret ever committed. The two keys are therefore mutually exclusive;
    `config.ValidateGit` refuses the combination rather than letting the weaker
    one silently win.
  - **`rw` additionally makes `.git/hooks` and `.git/config` writable**, and
    those are code the HOST's git executes on your next `git status` in that
    repo (`core.fsmonitor`, `core.sshCommand`, `post-checkout`, aliases). An
    `rw` share means you trust the sandbox's agent with your repository.
    `ro` cannot write anything: history is readable, the host repo is not
    modifiable, and `commit`/`checkout` inside the sandbox fail.
  - Pushing from inside is not enabled by either mode — no credentials or ssh
    agent reach a sandbox, and the forge still has to be in the egress
    allowlist.
  - Both modes are per-source and off by default; `SANDBOXER_NO_GIT=1` is the
    operator kill-switch that forces every source back to off regardless of the
    profile. `sandboxer show` names the share on the source's line.

  Note what this means for the host side: a narrowed sandbox's worktree holds
  the excluded files in full, one directory above the shared ones. That is
  deliberate (it is what lets an IDE open the branch), and it makes the absence
  of a `<slug>/` root share load-bearing rather than incidental — see
  `sandbox.Mounts` and the tests that pin it (`TestRunArgvNarrowedNeverMountsDest`).

  One subtlety a narrowed view must defend against: a virtio-fs share SOURCE is
  resolved on the HOST before sharing, so an `include` that names a
  directory which is (or traverses) a **symlink pointing outside the worktree**
  would share the host target past the wall. `sandbox.checkViewDirs`
  resolves every include's real path and refuses one that escapes the worktree
  (`TestCheckViewDirsRejectsSymlinkEscape`); symlinks INSIDE the shared content
  are fine — the guest dereferences those in its own namespace.

- **Isolated `$HOME` — host configs only by opt-in, and only as a copy.** Each
  sandbox has its own private home (`_home/<slug>` under the XDG state dir,
  `0700`), shared as `$HOME`. The host's real agent config — `~/.claude`,
  `~/.claude.json`, tokens, project history, MCP servers — and your `~/.ssh`,
  `~/.aws`, etc. are **never mounted** in, and API-key env vars are never
  passed through by default. A profile may set `hostConfigs = true` (the
  scaffolded config does) to opt into both: the sandbox home is **seeded**
  with a COPY of the agents' configs (a per-file merge on every
  create/enter/exec: missing files added, existing files never overwritten;
  claude's rotating OAuth pair is excluded — a copy breaks on refresh
  rotation and could hijack the host session), and the agents' auth env vars
  set on the host (API keys, a `claude setup-token` long-lived token) are
  passed into the environment of the process that needs them — the one-shot
  run, or each session `exec` (msb `--env`) — and never baked into the
  long-lived machine's configuration, so they are not sitting in its
  inspectable config and a rotated token reaches the next shell without a
  rebuild. Opt-in DLP hardening: `SANDBOXER_MSB_SECRETS=1` switches to msb's
  host-scoped `--secret KEY@host,…` (scoped to the egress allowlist, with
  `--on-secret-violation block-and-log`) — the guest then sees only a
  stand-in value that msb refuses to send anywhere off the list. It stays
  opt-in because the substitution direction is unverified upstream (a wrong
  assumption silently breaks agent auth) and the value binds at boot, so a
  rotation needs a machine restart. The `hostConfigs` trade is explicit
  either way: code running in that sandbox can read the copied credentials,
  and the egress policy is the wall between them and an exfiltration
  attempt — but it still cannot touch the host's real config (a hook written
  into the sandbox's settings.json never executes on the host), copies are
  per-sandbox (no cross-sandbox races), and an in-sandbox login/logout is
  never overwritten by a later seed. Keeping `hostConfigs` off returns the
  old posture: log in or export a key inside the sandbox when a task needs it.

- **Baked-in pi packages are image code, not fetched code.** Every sandbox's
  `~/.pi/agent/settings.json` is registered with the pi packages the toolbox
  image ships (today: pi's orchestration package), by absolute path into the
  image's read-only store — so pi loads a build that was pinned, hashed and
  reviewed at image-build time rather than resolving `npm:` at first run
  inside the sandbox. A pi package is code pi executes with the tools it has,
  which is the same trust level as the agent itself: it stays inside the
  sandbox's boundaries (mount set, egress allowlist, the VM), never runs on
  the host, and the entry is written only into the sandbox's own private home.
  The registration is a MERGE — a settings file that does not parse is left
  untouched — and `piPackages = false` (or `SANDBOXER_NO_PI_PACKAGES=1`) opts
  out entirely.

- **Clean guest environment.** The agent boots from an explicit, clean
  environment: it does **not** inherit your host shell, so an `AWS_*` /
  `GITHUB_*` / `*_TOKEN` left in your environment is invisible to it unless you
  wire it in. The config cannot wire one in behind your back:
  `sandboxer.nix` is evaluated with `restrict-eval` and an empty `NIX_PATH`, and
  under that setting `builtins.getEnv` returns `""` — a host variable cannot be
  read into the resolved profile. The config is still code you read before
  running an untrusted repo's copy (see "The config is code" below).

- **Resource caps.** Every machine is sized (a microVM must be — the default is
  a modest 2 vCPU / 4 GiB for the parallel-agent workload); the profile's
  `limits:` (`memory` / `cpus`) raises it. A runaway agent is bounded by the
  VM's allocation, not by the host's free RAM.

- **Egress: a name-bound network policy, default-deny.** With the allowlist on
  (the default), the machine boots `--no-net` — **no route at all** — plus one
  msb `--net-rule` pair per allowed domain: `allow@*.domain:tcp:80` and
  `allow@*.domain:tcp:443`. The rules are matched by **name at connect time**:
  the domain and its subdomains, those two ports, and nothing else — a raw-IP
  dial is refused **even for an allowed domain's own address**, so resolving a
  name and dialing the IP is not a bypass. There is no proxy sidecar and
  nothing of sandboxer's in the network path — enforcement is the runner's.
  An explicitly **empty** allowlist (`allowedDomains = [ ]`) means what it
  says: a fully offline machine, DNS included. The policy is part of the
  machine's create argv (folded into the session hash), so changing it
  recreates the machine — it cannot drift live. Disable deliberately with
  `egress.enabled = false` / `SANDBOXER_NO_EGRESS=1`.

### Where it stops (know these before you trust it)

- **The allowlist is name/port-level — no TLS inspection, no request
  filtering.** The policy engine decides which (domain, port) pairs a
  connection may target; it never looks inside the connection. Exfiltration
  **through an allowed domain** (an allowed API, a git host, a paste service on
  an allowed CDN) is out of scope, as is anything a permitted endpoint chooses
  to relay. Note the **default** list includes `cloudfront.net` — registry
  blobs (docker.io for some regions, all of `public.ecr.aws`) redirect there,
  but the entry admits *any* CloudFront distribution, i.e. an exfiltration
  channel anyone can stand up; set `egress.allowedDomains` without it if that
  outweighs registry pulls for you.

  > **`egress.proxy` opens exactly one door in the wall.** With egress on and
  > a proxy set, the machine still boots **default-deny with the allowlist
  > rules** — plus a single extra rule admitting the proxy's own host and
  > port, and the guest's HTTP(S) clients pointed at it (a loopback proxy URL
  > is rewritten to `host.microsandbox.internal`). Direct traffic — including
  > anything that ignores `HTTP(S)_PROXY` — is enforced by the VM. The
  > remaining trade is inherent to a CONNECT proxy: the VM sees only the dial
  > *to the proxy*, never the target names, so traffic that rides the proxy is
  > constrained by the **proxy** — a dumb tunnel imposes nothing. sandboxer
  > prints exactly this warning on every proxied run; pick a proxy you trust
  > to be that half of the boundary. With an empty allowlist the door is the
  > only rule: all egress rides the proxy.
  >
  > **`egress.enabled = false` with NO proxy is a fully open network** — no
  > allowlist and no proxy, so the agent has unrestricted outbound. sandboxer
  > does not silently accept this as "off": every run labels it
  > `egress=OPEN — unrestricted outbound` on the config line, and prints an
  > explicit `WARNING` when `hostConfigs` is also on (seeded host credentials
  > with an open exit is the worst-case pairing). It is never the safe default;
  > reach for it only when you truly want the agent on the open internet.

- **The worktree's `.git` pointer file is writable.** Git metadata is not
  shared (the whole hooks/config/object-store attack surface of earlier
  releases is gone with it), but a managed worktree's root still contains the
  one-line `.git` *pointer file*, and the share is rw. Corrupting or deleting
  it cannot execute anything — the path it names does not exist in the
  guest — but it can confuse the host-side worktree until you run
  `git worktree repair` (or `sandboxer recreate`). Also remember an **adopted**
  source (`srcs branch:` naming a branch already checked out in a worktree of
  your own) is that live tree, shared rw by explicit request — there is no
  isolation from it. Two checkouts are refused rather than adopted, because
  they would breach the boundary instead of merely widening it: the
  repository's **own** checkout — its `.git` is a real directory, so the share
  would put a writable git dir (hooks, config, filters) inside the sandbox —
  and a checkout belonging to **another sandbox**.

- **The config is code.** `sandboxer.nix` is EVALUATED on the host — under a
  restricted eval (no network access, imports/reads only inside the config's
  own directory), so it cannot exfiltrate or fetch, but it is still a program.
  Beyond that, `setup` is a shell script run in the sandbox, and the image
  overlay (`image.overlay`, a plain nixpkgs overlay file) executes nix code at
  image-build time **on the host** (the toolbox image is built with host nix).
  A committed `sandboxer.nix` therefore carries **executable content** on three
  levels — treat it as code and read an untrusted repo's config before
  `create`/`enter`.

- **`extraMounts` are read-write by default.** An `extraMounts` entry grants the
  agent access to that host path — **rw unless you set `mode: ro`**. Scope them
  narrowly; a broad rw share hands the agent a hole straight to the host FS.
  (virtio-fs shares directories only — a single-file mount is refused.)

- **Platform status.** Linux/KVM is where every release is exercised end to
  end. The macOS (Apple Silicon / Hypervisor.framework) and Windows (WSL2 +
  nested KVM) paths are **compiled and designed for, but not live-verified on
  real hardware** — the maintainer consciously shipped without that gate. On
  those platforms, treat the isolation as unproven until you have run
  `sandboxer doctor` and the basic lifecycle yourself; reports welcome.

- **Not a multi-tenant boundary.** Assume an adversarial agent can reach anything
  the sandbox can reach (the allowed domains, the shared paths). sandboxer
  raises the cost of a bad agent on your own machine; it is not an
  escape-proof jail — hypervisor and virtio-fs bugs exist too.
