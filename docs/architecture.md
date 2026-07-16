# Architecture

A one-page tour of how sandboxer is put together: where state lives on disk, the
lifecycle of a sandbox, how the toolbox container image is built, how outbound
network is fenced, and how the agent catalog stays single-source. For usage see
the [README](../README.md); for the trust model see [SECURITY.md](../SECURITY.md).

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
│   └── <repo>/<branch>/  #   grouped by repo, dir named after the branch,
│                         #   e.g. miko-java/feat/BDP-5291/
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
    └── <slug>/           # private $HOME mounted into the container,
                          #   0700 — holds in-sandbox login tokens
```

Key invariants (`internal/sandbox`, `internal/worktree`):

- **`<slug>/` holds one git worktree per source** (`srcs` entry): each source
  repo checked out at `<slug>/<repo>/<branch>/` — every entry names its branch
  explicitly (no default naming; a missing `branch:` is an error) and the
  worktree's directory is named after it — narrowed by gitignore-style
  `include` patterns via non-cone `git sparse-checkout`. Editing `branch:` is
  the one way to move a sandbox's worktree. The container mounts `<slug>/`
  (and any adopted worktrees) — **never git metadata** — so the sparse
  contents ARE the access boundary; work returns as ordinary branches
  committed on the host. The resolved list is recorded at
  `_meta/<slug>.srcs.json`; every enter/exec re-syncs it (a live session sees
  srcs edits immediately). A dropped source's worktree is removed when CLEAN
  (branch kept) and set aside under `_detached/` when it holds uncommitted
  work — naming that branch in `srcs` again re-attaches it (moved back, work
  intact; a set-aside checkout is never adopted in place). A registration
  whose directory was deleted by hand is pruned and the branch checked out
  fresh — `rm -rf ./sandboxes` self-heals on the next enter (the sandbox-dir
  generation `_meta/<slug>.gen` flips the session hash, so the pre-deletion
  session container is rebuilt instead of reused with dead mounts).
  sandboxer is **git-only**: a non-git source (or one with no commit) is
  rejected with an init hint.
- **`_home/<slug>` lives outside `<slug>/`** deliberately, so the agent's `$HOME`
  is never part of the worktree. It is `0700` because an in-sandbox `claude login`
  stores credentials there; the host's real `~/.claude`/tokens are never mounted.

## Sandbox lifecycle

```
  init ──────────►  scaffold sandboxer.nix   [optional]
                    │
  create <slug> ──► per srcs entry: git worktree at <slug>/<repo>/<branch>/ (sparse to
                    │   include patterns); mkdir _home/<slug>; snapshot
                    │   profile.json + srcs.json; register slug
                    ▼
  enter / exec ───► run the agent inside the container:
                    │   • bring up the egress allowlist sidecar (unless disabled)
                    │   • run the one-time `setup:` if its hash changed
                    │   • shell into the persistent session container, or one-shot
                    ▼
  review ─────────► per source, ON THE HOST: git -C <repo> log <branch>,
                    │   commit in the worktree, git diff / merge / cherry-pick
  stop ───────────► park the persistent session container (enter resumes it)
                    │
  rm ─────────────► git worktree remove + prune (managed sources only), delete
                    │   _home/<slug>, meta, logs, session container — KEEPS the
                    │   branches; adopted worktrees are never touched
  recreate --full ► also delete the branches sandboxer minted, for a clean slate
```

Notes:

- `enter` shells into a **persistent session container** (no managed
  multiplexer — tmux/zellij ship in the image as opt-in tools); `exec`
  reuses a running session; `stop` parks it; `rm` removes it. Full semantics
  and escape hatches: README "Persistent sessions"; decisions:
  [sessions-design.md](./sessions-design.md).
- The agent's work is a git branch in your repo's shared object store; you review
  and merge it with plain git. There is no `pull`/`push`/`diff`.
- The whole flow refuses to run **inside** the container — every command is
  deny-all there (sandboxer is a host tool, not baked into the image).

## Toolbox image (built with nix, no host nix)

The container backend runs the agents inside the `sandboxer-toolbox:latest` OCI
image, with the coding agents (claude, opencode, crush, aider, …) baked in. The
image is built **without nix on the host** (`internal/toolbox`):

```
  sandboxer image build
        │
        ▼
  host docker/podman ── runs ──► ephemeral nixos/nix:<pinned> container
        │                               │
        │   inject sandboxer binary     │ realizes the embedded flake
        │   into the build context      │ (assets/flake.nix):
        │                               │   • agents.nix     → the agents
        │                               │   • dockerTools.buildLayeredImage
        │                               ▼
        │                         image tarball
        ◄─── engine `load` ────────────┘
        │
        ▼
  sandboxer-toolbox:latest        (clean-by-default: builder container and the
                                   nixos/nix image are removed afterward;
                                   --cache keeps a nix-store volume for rebuilds)
```

Per-profile customization is **content-addressed**. A profile's `image:` section
(extra packages, a user `nix:` hook, input-pin overrides) produces a variant
tagged `sandboxer-toolbox:var-<12-hex>`, hashed over the effective input pins,
the package set (`tools` packs + `image.packages`), files, env and the overlay's content
(`internal/toolbox/spec.go`). Any change — a package, the hook's bytes, a pin —
is a new tag; identical profiles share one variant; the stock `:latest` is
untouched. `create`/`enter`/`exec` auto-build a missing image (stock or
variant) on first use.

The flake's `llm-agents`/`nixpkgs` inputs are **pinned**: a full 40-hex commit
pins exactly; `latest` is resolved to the remote head once, inside the builder,
and stamped into `~/.cache/sandboxer/image-pins.json` so later runs reuse it and
never silently drift. Only `image build --refresh` moves a `latest` pin.

## Egress allowlist (squid sidecar)

Outbound network is fenced by an allowlist forward-proxy — a stock **squid**
sidecar (the `sandboxer-proxy` image, built beside the toolbox image), NOT the
sandboxer binary (nothing of sandboxer's is ever in the network path):

```
   ┌───────────────────── internal network (no outbound) ──────────────────┐
   │                                                                        │
   │   agent container ──HTTP(S)_PROXY──►  squid allowlist sidecar ──► internet
   │   (git worktree + $HOME)               • host on allowlist  → dial     │
   │                                        • otherwise          → 403/deny │
   └────────────────────────────────────────────────────────────────────────┘
```

- The agent sits on an `--internal` network with **no direct route out**; its
  only exit is the squid sidecar, which permits only the hosts in
  `egress.allowedDomains` / `--allow-domains` (and their subdomains) and denies
  everything else. It is **fail-closed**: if the allowlist is required but the
  proxy can't start (or no domains are allowed) the run is **refused**, never
  opened. Disable deliberately with `egress.enabled = false` / `SANDBOXER_NO_EGRESS=1`.
- **`egress.proxy`** — a single upstream the sidecar chains all allowed traffic
  through (squid `cache_peer`, http:// only). A `localhost` proxy is rewritten to
  the host gateway so a proxy on the host is reachable from the sidecar.
- **`egress.routes`** — per-domain overrides: each route sends its domains
  through a dedicated upstream (a named `cache_peer`), never direct, so a routed
  peer being down fails closed. Every routed domain must also be in
  `allowedDomains`. Deterministic squid config; its fingerprint is folded into the
  session's freshness hash, so editing domains/proxy/routes recreates the session.
- With egress **off** the proxy **replaces** the allowlist — the agent talks to
  `egress.proxy` directly via `HTTP(S)_PROXY` (routes are ignored) and that proxy
  is trusted to police egress (http:// or https://).

## Agent registry (single source)

The agent catalog is one JSON file, `internal/registry/registry.json`, that is
both **embedded in the binary** (via `go:embed`) and **read by the nix flake**
(`builtins.fromJSON`) when baking the toolbox image:

```
        internal/registry/registry.json
        ┌──────────────┴───────────────┐
   go:embed                       builtins.fromJSON
   (the CLI: bin, auth-env,           (the flake: which llm-agents
    image inclusion)                   packages to bake into the image)
```

Each entry declares only the agent's binary name (`bin`), the env vars it reads
for auth (`authEnv` — informational; nothing is passed through from the host),
whether it ships in the image (`image`), and the `nixPackage` used to bake it
in. **Adding an agent
is one JSON entry** — never duplicate the catalog. `sandboxer agents` prints it.
