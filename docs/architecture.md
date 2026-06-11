# Architecture

A one-page tour of how sandboxer is put together: where state lives on disk, the
lifecycle of a sandbox, how the toolbox container image is built, how outbound
network is fenced, and how the agent catalog stays single-source. For usage see
the [README](../README.md); for the trust model see [SECURITY.md](../SECURITY.md).

## On-disk layout (`.sandboxer/`)

Everything sandboxer creates for a project lives under a single `.sandboxer/`
directory at the project root — including the committed project profile
(`config.yaml`) and image hook (`image.nix`). A generated `.sandboxer/.gitignore`
is an **allowlist** (`*` + `!.gitignore` + `!config.yaml` + `!image.nix`): it
commits only those three sandboxer-owned files while the leading `*` keeps the
generated state — working copies and login tokens alike — out of the user's repo.

```
<project-root>/
└── .sandboxer/
    ├── .gitignore            # allowlist: "*" + !.gitignore !config.yaml !image.nix
    ├── config.yaml           # the profile (optional; auto-discovered; committed)
    ├── image.nix             # the image hook the profile's image: points at (committed)
    ├── <slug>/                # one per sandbox: the agent's working dir,
    │                          #   holding only the pulled `deps` (flat copies)
    ├── _meta/                 # per-base + per-sandbox metadata
    │   ├── run.env            # SRC / MODEL / DOMAINS for this base
    │   ├── current            # the active sandbox slug (sandboxer use)
    │   ├── agents.list        # registered sandbox slugs, one per line
    │   ├── <slug>.profile.json   # resolved profile snapshot
    │   ├── <slug>.manifest.json  # what was pulled (for push/diff)
    │   ├── <slug>.meta           # per-sandbox run metadata
    │   └── <slug>.setup          # setup-script hash stamp (re-run gate)
    ├── _logs/                 # rotating per-sandbox logs (<slug>.json, .err, …)
    └── _home/
        └── <slug>/            # private $HOME mounted into the container,
                               #   0700 — holds in-sandbox login tokens
```

Key invariants (`internal/sandbox`):

- The **`<slug>/` working dir holds only the listed `deps`** — nothing is copied
  by default, and no git is involved. Each `dep` is located by path-suffix under
  the profile's `roots` and copied flat to `<slug>/<dep>`.
- **`_home/<slug>` lives outside `<slug>/`** deliberately, so the agent's `$HOME`
  is never swept up by `push` and copied back over a `dep` origin. It is `0700`
  because an in-sandbox `claude login` stores credentials there.
- The host's real `~/.claude` / tokens are **never** mounted in; each sandbox
  authenticates into its own `_home/<slug>`, so parallel sandboxes never share or
  race on one config.

## Sandbox lifecycle

```
  init ──────────►  scaffold .sandboxer/config.yaml (+ .sandboxer/image.nix)   [optional]
                    │
  create <slug> ──► mkdir .sandboxer/<slug>, _home/<slug>;
                    │   snapshot profile.json; PULL deps; register slug
                    ▼
  pull ───────────► copy deps in (suffix-match under roots; kept unless --force)
                    │
  enter / exec ───► run agent inside the container:
                    │   • bring up egress allowlist (unless disabled)
                    │   • run one-time `setup:` if its hash changed
                    │   • attach (persistent tmux session) or one-shot
                    │   • on exit: auto copy-back (push) the deps
                    ▼
  diff ───────────► show sandbox vs. each dep origin (no writes)
                    │
  push ───────────► copy deps back over their origins (wholesale overwrite)
                    │
  stop ───────────► park the persistent session container (enter resumes it)
                    │
  rm ─────────────► delete <slug>/, _home/<slug>, meta, logs, session container
```

Notes:

- `enter` attaches to a **persistent session container** (tmux inside): Ctrl-q
  detaches and the session — and any agent in it — keeps running; a later `enter`
  reattaches. `exec` reuses a running session; `stop` parks it; `rm` removes it.
  A session survives client disconnects but **not** a host/engine restart.
- `push` (and the automatic copy-back after `enter`/`exec`) **overwrites each
  origin wholesale** — there is no merge. Run `diff` first.
- The whole flow refuses to run the mutating commands while executing **inside**
  the container — only `pull`/`push`/`show`/`list`/`diff` are allowed there.

## Toolbox image (built with nix, no host nix)

The container backend runs the agents inside the `sandboxer-toolbox:latest` OCI
image, with the coding agents (claude, opencode, crush, aider, …) baked in. The
image is built **without nix on the host** (`internal/toolbox`):

```
  sandboxer build-image
        │
        ▼
  host docker/podman ── runs ──► ephemeral nixos/nix:<pinned> container
        │                               │
        │   inject sandboxer binary     │ realizes the embedded flake
        │   into the build context      │ (assets/flake.nix):
        │                               │   • llm-agents.nix  → the agents
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
untouched. `create`/`enter`/`exec`/`run` auto-build a missing image (stock or
variant) on first use.

The flake's `llm-agents`/`nixpkgs` inputs are **pinned**: a full 40-hex commit
pins exactly; `latest` is resolved to the remote head once, inside the builder,
and stamped into `~/.cache/sandboxer/image-pins.json` so later runs reuse it and
never silently drift. Only `build-image --refresh` moves a `latest` pin.

## Egress allowlist (forward-proxy sidecar)

Outbound network is fenced by an allowlist forward-proxy
(`internal/egress` + `internal/proxy`):

```
   ┌───────────────────────── internal network (no outbound) ──────────────┐
   │                                                                        │
   │   agent container ──HTTP(S)_PROXY──►  allowlist proxy sidecar ──► host/internet
   │   (deps + $HOME)                       (sandboxer _proxy mode)         │
   │                                          • host on allowlist  → dial   │
   │                                          • otherwise          → 403    │
   └────────────────────────────────────────────────────────────────────────┘
```

- The agent sits on an `--internal` network with **no direct route out**; its
  only exit is the proxy. The proxy is the **same sandboxer binary** in a hidden
  `_proxy` mode (no external dependency), permitting only the hosts in
  `network.allowedDomains` / `--allow-domains` and returning **403** for anything
  else.
- It is **fail-closed**: if the allowlist is required but the proxy can't start
  (or no domains are allowed), the run is **refused** rather than falling back to
  an open network. Disable deliberately with `egress: false` /
  `SANDBOXER_NO_EGRESS=1`.
- A configured upstream proxy (`proxy.http`/`proxy.https`) **replaces** the
  allowlist — sandboxer assumes that proxy is the boundary and does not start the
  sidecar.

## Agent registry (single source)

The agent catalog is one JSON file, `internal/registry/registry.json`, that is
both **embedded in the binary** (via `go:embed`) and **read by the nix flake**
(`builtins.fromJSON`) when baking the toolbox image:

```
        internal/registry/registry.json
        ┌──────────────┴───────────────┐
   go:embed                       builtins.fromJSON
   (the CLI: how to launch,            (the flake: which llm-agents
    creds/env, image inclusion)         packages to bake into the image)
```

Each entry declares how to launch the agent (interactive/headless command
templates), which host config dirs/env carry its credentials, and the
`nixPackage` used to bake it in. **Adding an agent is one JSON entry** — never
duplicate the catalog. `sandboxer agents` prints it.
