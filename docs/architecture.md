# Architecture

A one-page tour of how sandboxer is put together: where state lives on disk, the
lifecycle of a sandbox, the microVM backend, how the toolbox image is built, how
outbound network is fenced, and how the agent catalog stays single-source. For
usage see the [README](../README.md); for the trust model see
[SECURITY.md](../SECURITY.md).

## On-disk layout

The **committed** config lives at the project root; the worktrees live in the
project's `./sandboxes/` (auto-git-ignored, so working copies can never be
committed; relocatable via the profile's `worktreesDir`), and the rest of the
runtime state lives OUTSIDE the repo under an XDG state dir
(`$XDG_STATE_HOME/sandboxer/<project-id>`), keeping login tokens out of the
tree:

```
<project-root>/
└── sandboxer.nix         # the whole config, image customization included (auto-discovered;
                          #   committed; evaluated via host nix, restricted eval)

<project-root>/sandboxes/                   # the worktrees (auto-git-ignored)
├── <slug>/               # one per sandbox (relocatable: profile worktreesDir)
│   └── <branch>/<repo>/  #   grouped by branch, a dir per repo inside,
│                         #   e.g. feat/BDP-5291/miko-java/
└── _detached/            # dropped sources with UNCOMMITTED work, set aside
                          #   (clean ones are just removed — the branch stays;
                          #   sweep: sandboxer clean --detached --force, or
                          #   re-attached automatically when srcs names the
                          #   branch again)

$XDG_STATE_HOME/sandboxer/<project-id>/     # runtime state, outside the repo
├── _meta/
│   ├── run.env           # SRC / DOMAINS for this base
│   ├── current           # the active sandbox slug (sandboxer use)
│   ├── agents.list       # registered sandbox slugs, one per line
│   ├── <slug>.profile.json  # resolved profile snapshot
│   ├── <slug>.meta          # per-sandbox run metadata
│   ├── <slug>.setup         # setup-script hash stamp (re-run gate)
│   └── <slug>.gen           # sandbox-dir generation (bumped when the dir is
│                            #   created from nothing; folds into the session hash)
├── _logs/                # rotating per-sandbox logs (<slug>.json, .err, …)
└── _home/
    └── <slug>/           # private $HOME shared into the guest,
                          #   0700 — holds in-sandbox login tokens

$XDG_STATE_HOME/sandboxer/                  # host-wide (not per-project)
├── images/               # the toolbox build tars + .sha256 sidecars
│                         #   (msb imports from here; see "Toolbox image")
└── machines/microsandbox/<name>.json  # per-machine session records: slug,
                          #   base dir, config hash, image id, mount ids
```

Key invariants (`internal/sandbox`, `internal/worktree`):

- **`<slug>/` holds one git worktree per source** (`srcs` entry): each source
  repo checked out **complete** at `<slug>/<branch>/<repo>/` — every entry names
  its branch explicitly (no default naming; a missing `branch:` is an error) and
  the branch names the dirs the worktree sits under. Editing `branch:` is the one way
  to move a sandbox's worktree. What the SANDBOX sees is decided by the mount
  set, not by the tree (`sandbox.Mounts`): with no `include`, `<slug>/` is
  shared whole; with `include`, `<slug>/` is **not shared at all** and each
  listed directory is shared at its own path instead. Either way **no git
  metadata is shared** — the mount set IS the access boundary; work returns as
  ordinary branches committed on the host. Keeping the host tree complete is
  what lets an IDE open a narrowed sandbox's branch. The resolved list is recorded at
  `_meta/<slug>.srcs.json`; every enter/exec re-syncs it (a live session sees
  srcs edits immediately). A dropped source's worktree is removed when CLEAN
  (branch kept) and set aside under `_detached/` when it holds uncommitted
  work — naming that branch in `srcs` again re-attaches it (moved back, work
  intact; a set-aside checkout is never adopted in place). A registration
  whose directory was deleted by hand is pruned and the branch checked out
  fresh — `rm -rf ./sandboxes` self-heals on the next enter (the sandbox-dir
  generation `_meta/<slug>.gen` flips the session hash, so the pre-deletion
  session machine is rebuilt instead of reused with dead mounts).
  sandboxer is **git-only**: a non-git source (or one with no commit) is
  rejected with an init hint.
- **Every source occupies a slot under `<slug>/`** — including an adopted one.
  Git allows a branch in only one worktree, so a `branch:` already checked out
  in a worktree of the user's is ADOPTED: shared at its own host path *and*
  symlinked at `<slug>/<branch>/<repo>`. The link is not decoration — `<slug>/`
  is the guest's workdir, so a source reachable only from somewhere else on
  the host is one the user listed and cannot find. Two checkouts are refused
  instead (`sandbox.checkAdoptable`): the repository's **own** checkout (its
  `.git` is a real directory, so the share would carry a writable git dir into
  the sandbox and hand the agent the tree the user works in) and one owned by
  **another sandbox** (found host-wide via `Projects()` — two sandboxes sharing
  one working tree is the opposite of the point). Both errors name the fix:
  give that source its own branch.
- **`_home/<slug>` lives outside `<slug>/`** deliberately, so the agent's `$HOME`
  is never part of the worktree. It is `0700` because an in-sandbox `claude login`
  stores credentials there; the host's real `~/.claude`/tokens are never mounted.

## Sandbox lifecycle

```
  init ──────────►  scaffold sandboxer.nix   [optional]
                    │
  create <slug> ──► per srcs entry: full git worktree at <slug>/<branch>/<repo>/
                    │   (include narrows the SHARES, not the tree); mkdir _home/<slug>; snapshot
                    │   profile.json + srcs.json; register slug
                    ▼
  enter / exec ───► run the agent inside the microVM:
                    │   • boot the machine with its network policy baked in
                    │     (default-deny + per-domain rules, unless disabled)
                    │   • run the one-time `setup:` if its hash changed
                    │   • shell into the persistent session machine, or one-shot
                    ▼
  review ─────────► per source, ON THE HOST: git -C <repo> log <branch>,
                    │   commit in the worktree, git diff / merge / cherry-pick
  stop ───────────► park the persistent session machine (enter resumes it)
                    │
  rm ─────────────► git worktree remove + prune (managed sources only), delete
                    │   _home/<slug>, meta, logs, session machine — KEEPS the
                    │   branches; adopted worktrees are never touched
  recreate --full ► also delete the branches sandboxer minted, for a clean slate
```

Notes:

- `enter` attaches an in-guest **tmux session** (`tmux -L sandboxer`,
  system /etc/tmux.conf: mouse on, rc.sh panes) inside a **persistent session
  machine**; `exec` reuses a running session; `stop` parks it; `rm` removes
  it. Full semantics
  and escape hatches: README "Persistent sessions"; decisions:
  [sessions-design.md](./sessions-design.md).
- The agent's work is a git branch in your repo's shared object store; you review
  and merge it with plain git. There is no `pull`/`push`/`diff`.
- The whole flow refuses to run **inside** the sandbox — every command is
  deny-all there (sandboxer is a host tool, not baked into the image).

## Isolation backend (`internal/backend`)

There is exactly **one backend**: `microsandbox` — a real microVM per sandbox
on [microsandbox](https://microsandbox.dev) (`msb`, libkrun: KVM on Linux, HVF
on macOS). sandboxer shells out to `msb` the way it once shelled out to
docker/podman, so the backend stays a set of **pure argv builders** a golden
test pins without a hypervisor. The layout:

- `msb.go` — the msb dialect: create/exec/run argv builders, the network
  policy (`msbNetworkArgs`), auth-env/`--secret` rendering, the msb image-store
  handoff, and the msb-specific preflights (`/tmp` shares, file mounts,
  fractional limits).
- `vm_session.go` — the session lifecycle over the pure `planSession` policy:
  hash the canonical create argv, inspect, then exec / start / recreate /
  create; plus the sweeps (remove-all, states, orphans).
- `vm_state.go` — the host-side machine records
  (`<state>/machines/microsandbox/<name>.json`): the identity a session needs
  (slug, base dir, config hash, image id, mount ids). msb labels carry the
  same identity for `msb list` discoverability, but the record is the source
  of truth — one mechanism, one set of sweeps.
- `vm_image.go` — the build-artifact image store (`<state>/images/<name>.tar`
  + `.sha256` sidecar); the tar's content id is the freshness authority that
  makes a rebuilt image read as stale.

Retired values of `backend:` (docker/podman/"auto"/"", the smolvm `microvm`,
`native`) fail with a migration error — nothing silently falls back, which
would quietly change the isolation boundary.

## Toolbox image (prebuilt; local builds with host nix)

The agents run inside the `ghcr.io/irasikhin/sandboxer-toolbox:latest` OCI
image, with the coding agents (claude, opencode, crush, aider, …) baked in.
The stock image comes **prebuilt from GHCR** — published by
`.github/workflows/image.yml` nightly (the agent pins re-resolve at build, so
`latest` tracks their releases) and per release tag — and msb **pulls and
caches it host-side on first create** (the pull honors the shell's
`HTTP(S)_PROXY`). A create only pulls a ref *missing* from msb's store;
`sandboxer image pull` refreshes a moved `latest` (the fresh digest reads as
stale, so the next `enter` recreates the session).

The **local build** covers a profile's `var-` variant (never published) and
offline hosts. It runs with **host nix** — nix is already a hard requirement
of the CLI (it evaluates `sandboxer.nix`), so there is no builder container
and no builder guest (`internal/toolbox`):

```
  sandboxer image build
        │
        │   write flake + agents/          realizes the embedded flake
        │   tools/overlay into a ctx ────► (assets/flake.nix) with HOST nix:
        │                                    • agents.nix → the agents
        │                                    • dockerTools.buildLayeredImage
        │                                          │
        │                                          ▼
        │                                    image tarball
        │                                          │
        ▼                                          ▼
  <state>/images/<tag>.tar  ◄── stored (+ .sha256 sidecar; the content id
        │                        is the image-freshness authority)
        ▼
  msb load ── imports it into microsandbox's own image store, which is what
              `msb create` boots — after that every create is boot-only
```

`create`/`enter`/`exec` auto-build a missing `var-` variant on first use
(`SANDBOXER_NO_AUTOBUILD=1` disables); a rebuilt tar makes the cached msb copy
read as stale (a load-marker sidecar records which tar was imported), so a
rebuild + `recreate` is how a new image reaches a live sandbox. An explicitly
built STOCK image lands in msb's store under the prebuilt ref, so a create
boots the local copy instead of pulling — the offline escape hatch. `image rm`
drops both the msb-cached image and the tar.

Per-profile customization is **content-addressed**. A profile's `image:` section
(extra packages, files/env, a nixpkgs overlay, input-pin overrides) produces a
variant tagged `sandboxer-toolbox:var-<12-hex>`, hashed over the effective input
pins, the package set (`tools` packs + `image.packages`), files, env and the
overlay's content (`internal/toolbox/spec.go`). Any change — a package, the
overlay's bytes, a pin — is a new tag; identical profiles share one variant;
the stock prebuilt `:latest` is untouched. `create`/`enter`/`exec` auto-build
a missing variant on first use.

The flake's `nixpkgs` input — the single input everything, agents included,
comes from (prebuilt on cache.nixos.org; pi alone is vendored in the binary
and grafted in by an overlay) — **tracks the remote head by default**:
`image build` re-resolves it (on the host, via `git ls-remote`) and stamps the
result into `~/.cache/sandboxer/image-pins.json`; `enter`/`exec` only ever
reuse the stamp, so nothing drifts between explicit builds.
`image build --no-refresh` builds from the existing stamp; a full 40-hex
commit in `image.nixpkgsRev` pins the input exactly.

## Egress (name-bound network policy)

Outbound network is fenced by microsandbox's **policy engine** — rules on the
machine itself, matched by NAME at connect time. There is no proxy sidecar and
nothing of sandboxer's in the network path (`backend.msbNetworkArgs` renders
the rules):

```
   ┌──────────────── microVM, boots --no-net (no route at all) ────────────┐
   │                                                                        │
   │   agent ── connect(host, port) ──►  msb --net-rule engine  ──► internet
   │   (git worktrees + $HOME shares)     • allow@*.domain:tcp:80/443 → dial│
   │                                      • anything else (raw IPs too)     │
   │                                        → refused                       │
   └────────────────────────────────────────────────────────────────────────┘
```

- **Allowlist on (the default):** the machine boots `--no-net` — default-deny —
  plus one `--net-rule allow@*.domain:tcp:80,allow@*.domain:tcp:443` per
  domain in `egress.allowedDomains` / `--allow-domains`: the domain **and its
  subdomains**, HTTP and HTTPS, nothing else. Rules are name-bound, so a
  raw-IP dial is refused even for an allowed domain's own address. An
  explicitly **empty** allowlist is `--no-net` alone — a fully offline
  machine, a valid state. Disable deliberately with `egress.enabled = false`
  / `SANDBOXER_NO_EGRESS=1` (an open network; every run labels it).
- **`egress.proxy`** — proxy-delegated egress: the network stays open and the
  guest's HTTP(S) clients are pointed at the proxy (`HTTP_PROXY`/`HTTPS_PROXY`
  env; `egress.noProxy` → `NO_PROXY`) — the proxy IS the control point, and an
  `allowedDomains` set alongside is enforced by the proxy, not the VM
  (sandboxer warns). A loopback proxy URL is rewritten to
  `host.microsandbox.internal` (the guest's loopback is its own stack) and the
  policy set to `allow@public` plus the host on exactly the proxy's port.
- The policy lives in the **create argv**, so it folds into the session hash:
  editing domains/proxy/egress recreates the machine — enforcement can never
  drift from the config on a live session.
- **`egress.routes`** (per-domain upstream proxies) was a squid `cache_peer`
  feature and is retired with the container backend; the config key errors
  with a migration hint.

## Agent registry (single source)

The agent catalog is one JSON file, `internal/registry/registry.json`, that is
both **embedded in the binary** (via `go:embed`) and **read by the nix flake**
(`builtins.fromJSON`) when baking the toolbox image:

```
        internal/registry/registry.json
        ┌──────────────┴───────────────┐
   go:embed                       builtins.fromJSON
   (the CLI: bin, auth-env,           (the flake: which nixpkgs
    image inclusion)                   attrs to bake into the image)
```

Each entry declares only the agent's binary name (`bin`), the env vars it reads
for auth (`authEnv` — passed through from the host when the profile sets
`hostConfigs = true`),
whether it ships in the image (`image`), and the `nixPackage` used to bake it
in. **Adding an agent
is one JSON entry** — never duplicate the catalog. `sandboxer agents` prints it.
