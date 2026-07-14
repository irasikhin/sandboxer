# Architecture

A one-page tour of how sandboxer is put together: where state lives on disk, the
lifecycle of a sandbox, how the toolbox container image is built, how outbound
network is fenced, and how the agent catalog stays single-source. For usage see
the [README](../README.md); for the trust model see [SECURITY.md](../SECURITY.md).

## On-disk layout

The **committed** config lives at the project root; all **runtime state**
lives OUTSIDE the repo under an XDG state dir
(`$XDG_STATE_HOME/sandboxer/<project-id>`), so working copies and login tokens
can never be committed. The repo carries only two sandboxer-owned files:

```
<project-root>/
├── sandboxer.yaml        # the profile (optional; auto-discovered; committed)
└── sandboxer-image.nix   # the image hook the profile's image: points at (committed)

$XDG_STATE_HOME/sandboxer/<project-id>/     # runtime state, outside the repo
├── <slug>/               # one per sandbox: a git worktree on branch feat/<slug>-sb
├── _meta/
│   ├── run.env           # SRC / DOMAINS for this base
│   ├── current           # the active sandbox slug (sandboxer use)
│   ├── agents.list       # registered sandbox slugs, one per line
│   ├── <slug>.profile.json  # resolved profile snapshot
│   ├── <slug>.meta          # per-sandbox run metadata
│   └── <slug>.setup         # setup-script hash stamp (re-run gate)
├── _logs/                # rotating per-sandbox logs (<slug>.json, .err, …)
└── _home/
    └── <slug>/           # private $HOME mounted into the container,
                          #   0700 — holds in-sandbox login tokens
```

Key invariants (`internal/sandbox`, `internal/worktree`):

- **`<slug>/` is a git worktree** of the project repo on branch `feat/<slug>-sb`
  (off HEAD), optionally narrowed by the profile's `deps` to a subset of
  repo-relative directories via cone `git sparse-checkout` (empty = the whole
  repo). The agent's work returns as an ordinary branch — no copy-in, no
  push-back. sandboxer is **git-only**: a non-git project (or one with no commit)
  is rejected with an init hint.
- **`_home/<slug>` lives outside `<slug>/`** deliberately, so the agent's `$HOME`
  is never part of the worktree. It is `0700` because an in-sandbox `claude login`
  stores credentials there; the host's real `~/.claude`/tokens are never mounted.

## Sandbox lifecycle

```
  init ──────────►  scaffold sandboxer.yaml (+ sandboxer-image.nix)   [optional]
                    │
  create <slug> ──► add a git worktree on branch feat/<slug>-sb (sparse to deps);
                    │   mkdir _home/<slug>; snapshot profile.json; register slug
                    ▼
  enter / exec ───► run the agent inside the container:
                    │   • bring up the egress allowlist sidecar (unless disabled)
                    │   • run the one-time `setup:` if its hash changed
                    │   • attach (persistent tmux session) or one-shot
                    ▼
  review ─────────► the work is a branch: git -C <repo> log feat/<slug>-sb,
                    │   git diff / merge / cherry-pick
  stop ───────────► park the persistent session container (enter resumes it)
                    │
  rm ─────────────► git worktree remove + prune, delete _home/<slug>, meta, logs,
                    │   session container — KEEPS the sandbox branch
  recreate --full ► also delete the branch, for a clean slate
```

Notes:

- `enter` attaches to a **persistent session container** (tmux inside); `exec`
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
the package set (`tools` packs + `extraPkgs`) and the nix file's content
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
  `network.allowedDomains` / `--allow-domains` (and their subdomains) and denies
  everything else. It is **fail-closed**: if the allowlist is required but the
  proxy can't start (or no domains are allowed) the run is **refused**, never
  opened. Disable deliberately with `egress: false` / `SANDBOXER_NO_EGRESS=1`.
- **`network.proxy`** — a single upstream the sidecar chains all allowed traffic
  through (squid `cache_peer`, http:// only). A `localhost` proxy is rewritten to
  the host gateway so a proxy on the host is reachable from the sidecar.
- **`network.routes`** — per-domain overrides: each route sends its domains
  through a dedicated upstream (a named `cache_peer`), never direct, so a routed
  peer being down fails closed. Every routed domain must also be in
  `allowedDomains`. Deterministic squid config; its fingerprint is folded into the
  session's freshness hash, so editing domains/proxy/routes recreates the session.
- With egress **off** the proxy **replaces** the allowlist — the agent talks to
  `network.proxy` directly via `HTTP(S)_PROXY` (routes are ignored) and that proxy
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

Each entry declares only the agent's binary name (`bin`), the env vars that carry
its credentials (`authEnv`, passed through when set on the host), whether it ships
in the image (`image`), and the `nixPackage` used to bake it in. **Adding an agent
is one JSON entry** — never duplicate the catalog. `sandboxer agents` prints it.
