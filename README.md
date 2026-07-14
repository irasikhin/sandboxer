# sandboxer

[![CI](https://github.com/irasikhin/sandboxer/actions/workflows/ci.yml/badge.svg)](https://github.com/irasikhin/sandboxer/actions/workflows/ci.yml)
[![Coverage](https://img.shields.io/badge/coverage-%E2%89%A590%25-brightgreen.svg)](#testing)
[![Go](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Run **several autonomous coding agents in parallel** — or work by hand in a
single sandbox — each fully isolated, on your **local Linux machine**. A Go CLI,
shipped as a static binary, `go install`, or a Nix flake. Human designs, AI
drives.

> 🧪 **Experimental — a personal project, not a product.** sandboxer is in
> **active development** and is **not intended for production use**. It's my
> personal tool for running AI coding agents locally, shipped with no stability
> or support guarantees. Use at your own risk.

> ⚠️ **Pre-1.0.** CLI flags, the `sandboxer.yaml` schema and the on-disk layout may change
> between minor versions until 1.0. Sandboxes expose **sources** — git repos
> checked out into host-side worktrees, narrowed by `srcs` include patterns
> (see below). Any future change will be called out in the changelog.

## How it works

A **sandbox** exposes **sources**: git repos checked out into per-sandbox
worktrees (branch `feat/<slug>-sb`) under the state dir. Run sandboxer inside a
repo and it just works — **zero config**: the whole repo is the one source. The
container sees **only the files the sources select — git itself never enters
it**: no `.git` mounts, no history, no hooks. The agent edits files; the edits
land live in the host-side worktree, where you review and commit them with
plain git. Your working tree and current branch are never touched, and nothing
is copied.

- **Sandbox** — a set of sources materialized as git worktrees under one dir.
- **slug** — a short sandbox name (`feat`, `bugfix-auth`, …), set at `create`.
- **srcs (optional)** — the sources: each entry is `src:` (path to a repo root,
  also other repos), an optional `include:` (gitignore-style patterns — only
  matching files exist in the sandbox) and an optional `branch:` (adopt an
  existing branch/worktree). Omit srcs to get `[{src: .}]` — this repo, whole.
  Editing srcs applies on the next `enter`/`exec` — a running session sees the
  change live, no recreate.
- **review** — on the HOST, per source repo: `git -C <repo> log feat/<slug>-sb`,
  `git add`/`commit` in the worktree, then merge or cherry-pick.

sandboxer is **git-only**: every `src` must be a git repo with at least one
commit (`git init && git add -A && git commit -m init`). Non-git trees come in
via `extraMounts`.

Isolation backend — a **docker / podman** container built from a toolbox image
with the agents baked in (claude, opencode, crush, aider, pi, gemini). Any of
them; each sandbox gets its own isolated home, and network, proxy and
credentials are wired per config.

For the full picture — on-disk layout, sandbox lifecycle, how the toolbox image
is built and cached, the egress proxy and the agent registry — see
[docs/architecture.md](./docs/architecture.md). Hitting a wall? See
[docs/troubleshooting.md](./docs/troubleshooting.md).

## Install

Linux only. The container engine (docker or podman) is **not bundled** — it
comes from the host.

```bash
nix run    github:irasikhin/sandboxer -- help                   # try without installing
nix profile install github:irasikhin/sandboxer                  # Nix
go install github.com/irasikhin/sandboxer/cmd/sandboxer@latest  # Go
```

Or grab a [pre-built binary](https://github.com/irasikhin/sandboxer/releases)
(linux amd64/arm64).

## Quick start

```bash
sandboxer config init                     # scaffold a commented sandboxer.yaml + sandboxer-image.nix to edit (optional)
sandboxer create feat                     # create a sandbox named "feat" (worktree on branch feat/feat-sb)
sandboxer enter  feat                     # attach a shell (persistent session; Ctrl-q detaches)
sandboxer exec   feat -- claude           # run an agent/command inside it
git log feat/feat-sb                      # the work is an ordinary branch (commit it on the host)
sandboxer stop   feat                     # park the session container (enter resumes it)
sandboxer list                            # status of all sandboxes (alias: sandboxer status)
sandboxer rm     feat                     # delete the sandbox and its session (keeps the branch)
```

A profile is optional (empty = the whole repo). To narrow the sandbox or add
setup/tools/env, drop a `sandboxer.yaml` in the cwd (auto-discovered),
pass one with `-f` (a file, a directory of profiles, or a
[named profile](#named-profiles) from `~/.config/sandboxer/profiles/`), or refer
to a stored profile by name; the sandbox slug then comes from the profile's
`name:`.

Commands group into three activities (also shown that way in `--help`): forming
the **image** (`sandboxer image build|edit|rm`) and **config**
(`sandboxer config init|edit|validate|get|set|unset`, plus
`sandboxer profile list|use` for picking one), managing **state** (`clean`),
and **entering/working** in the sandbox
(`create` / `enter` / `exec` / `stop` / `rm` / `list` / `use`).

## Config vs data

The committed config is just two files at your repo root — `sandboxer.yaml` and
the optional `sandboxer-image.nix` — checked in as-is. All runtime
state (per-sandbox working copies, the private agent homes, logs and metadata)
lives **outside** the repo under the XDG state dir
(`$XDG_STATE_HOME/sandboxer/<project>`, default `~/.local/state/...`), so secrets
and scratch data can never be committed by accident. `sandboxer clean` wipes that
state for the project; the config stays.

## How changes flow

Changes flow through git — on the host. Each source is a **git worktree** on
branch `feat/<slug>-sb`; the container's edits appear there live (bind mount),
and **you** commit/review them with plain git (`git -C <worktree> add/commit`,
`git log`/`git diff`/`git merge feat/<slug>-sb`). The container itself has no
git access at all: no object store, no hooks, no history. There is no copy-in
and no push-back. Teardown (`rm`, `recreate`) keeps the branches;
`recreate --full` deletes the auto-named ones for a fresh start (never a
branch you set via `srcs branch:`).

## Persistent sessions

By default `enter` attaches to a **persistent session container** (tmux
inside): **Ctrl-q detaches**, the session — and any agent running in it —
keeps going, and a later `sandboxer enter feat` reattaches (`--session <name>`
opens extra tmux sessions in the same container). `exec` reuses a running
session; `stop` parks the container for a later resume; `rm` removes it along
with the sandbox. `list`'s STATE column shows `running`/`stopped`/`-` per
sandbox. When the profile changes or the toolbox image is rebuilt, the next
`enter` recreates an idle session (and refuses while others are attached).

Escape hatches back to one-shot containers: `--ephemeral` (enter/exec),
`session: ephemeral` in the profile, or `SANDBOXER_SESSION=ephemeral` (the env
wins over the profile — an operator kill-switch). A session survives client
disconnects, **not** a host/engine restart — resuming the agent's conversation
is the agent's job (e.g. `claude --continue`). Design notes:
[docs/sessions-design.md](./docs/sessions-design.md).

## Config

Scalars come from **flags** and `SANDBOXER_*` env vars:

| Setting | Flag | Env |
|---------|------|-----|
| backend | `--backend` | `SANDBOXER_BACKEND` (default `docker`; `docker\|podman` pins that engine when installed, else falls back to whichever is) |
| session mode | `--ephemeral` | `SANDBOXER_SESSION` (default `persistent`; the env wins over a profile's `session:`) |
| egress domains | `--allow-domains a,b` | `SANDBOXER_DOMAINS` |
| disable egress | — | `SANDBOXER_NO_EGRESS=1` |
| skip auto-scaffold | — | `SANDBOXER_NO_SCAFFOLD=1` (create/enter writes a default `sandboxer.yaml` otherwise) |
| container engine | — | `SANDBOXER_ENGINE` (default: auto-detect docker→podman) |
| container user | — | `SANDBOXER_CONTAINER_USER` (default: host uid:gid; empty omits `--user` — macOS escape hatch, see [docs/macos.md](docs/macos.md)) |
| image | — | `SANDBOXER_IMAGE` (default `sandboxer-toolbox:latest`) |
| resource caps | — | `SANDBOXER_MEM` / `SANDBOXER_CPU` (or the profile's `limits:` — see below) |

The sandbox container's resource caps come from the profile's `limits:` block
(`memory`, `cpus`, `pids`), overriding the `SANDBOXER_MEM`/`SANDBOXER_CPU` env
defaults; `pids` (a `--pids-limit`, bounding fork-bomb blast radius) is
profile-only. Empty means uncapped.

Structured fields (`srcs`, `extraMounts`, `env`, `setup`, `tools`, `image`, `limits`) live in an **optional**
`sandboxer.yaml`. Point at it with `-f`/`--config`, which accepts a **file**, a
**directory** of profiles, or the **name** of a profile in the store (see
[Named profiles](#named-profiles)); with nothing given, a `sandboxer.yaml` in
the cwd is auto-discovered. See `examples/config.yaml`,
`examples/with-srcs.yaml` and `examples/profiles/`.

> `sandboxer.yaml` and `sandboxer-image.nix` are meant to be **committed**
> with your repo — don't gitignore them (`sandboxer doctor` warns when a
> rule hides them).

```yaml
name: feature-x
backend: docker
network:
  allowedDomains: [api.anthropic.com, registry.npmjs.org, github.com]
srcs:                    # the sources the sandbox sees (this repo, narrowed)
  - src: .
    include: ["/src/lib/", "/shared/proto/"]
setup: |                 # one-time prep, run once inside the sandbox
  npm ci
tools: [node, go]        # runtime tool packs baked into a per-profile image
```

Each `srcs` entry is a repo (`src:` — `.` or a path to another repo's root)
narrowed by `include:` **gitignore-style patterns** (`/dir/`, `*.md`, `!…`;
non-cone `git sparse-checkout` under the hood — omit for the whole repo), and
optionally pinned with `branch:` — naming a branch whose worktree already
exists (even your main checkout) **adopts** it instead of creating one. Editing
`srcs` applies on the next `enter`/`exec` and is visible to a **running**
session immediately. To bring in **non-git** trees, use `extraMounts`.

`setup` is a one-time shell script (`bash -lc`) run inside the sandbox before
you take over — e.g. `npm ci`, a build, a DB seed. It runs on the first
`enter`/`exec` and again only when the script changes (a per-sandbox
stamp tracks it), under the **same egress allowlist** as the sandbox (so a
network install needs its domains allowed). A failed setup is fatal by default;
skip it with `--no-setup`. The baked shell can also be extended without
rebuilding the image: drop `*.sh` files in `/etc/sandboxer/rc.d/` (image
plugins) or write `~/.config/sandboxer/rc` (per-sandbox `$HOME`).

> ⚠️ `setup:` and `image.nix` (below) both run **arbitrary code the profile
> points at** — setup inside the sandbox under its egress allowlist, the nix
> hook inside the throwaway image-builder container with full network. That is
> the intended trust level for *your own* profiles; treat a third-party
> profile like a shell script someone sent you — read it before running it.

`tools` names language/runtime packs (`node`, `python`, `go`, `rust`, … — see
`internal/registry/tools.json`) baked into a **per-profile toolbox image**
variant, built on demand and content-addressed (see
[Custom toolbox image](#custom-toolbox-image-image)).

MCP servers need no sandboxer wiring: the sandbox contains your repo's files,
so agent-level MCP config committed there (e.g. a `.mcp.json`) works as-is —
just add the servers' domains to `network.allowedDomains`.

### Editing config from the CLI

`sandboxer config set|get|unset` edit the config file **in place, preserving
comments and key order** (only long lines may re-wrap once), so you never have
to open an editor for one knob:

```bash
sandboxer config set backend podman                    # dotted keys into the profile
sandboxer config set network.allowedDomains '[api.anthropic.com, github.com]'
sandboxer config set env.NODE_ENV production           # one env var
sandboxer config set limits.memory 4G -p web           # target a profiles: section
sandboxer config get network.proxy                     # read one value
sandboxer config unset network.proxy                   # remove a key
```

Without `-p` the target section is the **active sandbox** (`sandboxer use`)
when it names one, then the file's `default:`, then its sole profile. `set` is
strictly validated in memory before writing — a bad key or type never lands on
disk. Existing sandboxes pick changes up on their next `enter`/`exec` —
`srcs` included: even a running session sees new sources live.

### Custom toolbox image (`image:`)

A profile can customize the toolbox image itself — extra packages, files, env,
even a nixpkgs overlay — **without nix on your machine** (the same builder
container does everything) and without forking the image. `sandboxer config init`
scaffolds this section together with an inert, fully-commented
`sandboxer-image.nix` hook next to the profile at the repo root, ready to edit:

```yaml
image:
  extraPkgs: [gh, python3Packages.requests]  # extra nixpkgs attrs baked in
  nix: image.nix                             # build hook (path relative to this file)
  llmAgentsRev: latest                       # input pin override: latest | full commit hash
  # nixpkgsRev: <commit>                     # empty = the pin embedded in the binary
```

The customization is **content-addressed**: the sandbox runs
`sandboxer-toolbox:var-<12hex>` — hashed over the effective input pins, the
package set (`tools` packs + `extraPkgs`) and the nix file's content —
auto-built on first use and shared by identical profiles; the stock
`sandboxer-toolbox:latest` is untouched. Any change (a package, the hook's
content, a pin) is a new tag, and an idle persistent session recreates itself
on the next `enter`.

`image.nix` is a function `{ pkgs }: { … }` returning any of four keys —
unknown keys abort the build, so a typo never silently drops a customization:

```nix
{ pkgs }:
{
  overlay = final: prev: { };     # nixpkgs overlay, applied first
  packages = [ pkgs.httpie ];     # store paths to bake in (pkgs has the overlay applied)
  files."/etc/motd" = "hello\n";  # text files at absolute paths
  env.MY_FLAG = "1";              # OCI env (overrides a same-named default)
}
```

The contract is two-phase: `overlay` is read from a first call with base
nixpkgs, then the function is called again with the overlay'd `pkgs` for
`packages`/`files`/`env` — packages see the overlay, but the overlay cannot
reference its own result. Full commented example:
[examples/profiles/custom-image.yaml](./examples/profiles/custom-image.yaml) +
[image.nix](./examples/profiles/image.nix).

`llmAgentsRev`/`nixpkgsRev` move the image's flake-input pins — e.g. pick up
newer agents without waiting for a sandboxer release. A full 40-hex commit
hash pins exactly; `latest` is resolved to the remote head **once**, inside the
builder container at build time, and stamped into the per-user pins cache
(`~/.cache/sandboxer/image-pins.json`). `enter`/`exec` reuse the stamp and
never re-resolve; only `sandboxer image build --refresh` moves it.

### Multiple profiles in one file

Instead of one profile per file, a `sandboxer.yaml` can hold many under a
`profiles:` map. Every section is **self-contained** — there is no shared
defaults layer and no merging between files. To reuse a block between sections,
use plain **YAML anchors** — anchor one (`&api`) and merge it into another
(`<<: *api`); no special key is needed, and the node's own fields win over the
merge. `default:` names the one used when you don't name a section. The flat
one-profile form above still works. See `examples/multi-profile.yaml`.

```yaml
profiles:
  api: &api                # sandboxer create api
    backend: docker
    network:
      allowedDomains: [api.anthropic.com, github.com]
    srcs: [{src: ., include: ["/shared/proto/"]}]
  api-prod:                # sandboxer create api-prod
    <<: *api               # inherit api via the anchor, then override
    env: { NODE_ENV: production }

default: api               # sandboxer create   (no name → api)
```

`sandboxer create <name>` selects the section by name (that name becomes the
sandbox slug); a name that matches no section falls back to the store / a plain
slug. With no name, `create` uses the `default:` (or sole) profile.

### Named profiles

Keep reusable profiles as files and select them by **name** instead of by path.
A profile's name is its file's base name (`web.yaml` → `web`) unless the file
sets an explicit `name:`, which wins. A name resolves project config → store:

```bash
sandboxer create ./feat.yaml          # an explicit file
sandboxer create api  -f ./envs        # the profile named "api" inside a directory
sandboxer create web                   # a named profile from the global store
sandboxer profile list                 # list the store; `profile list -f ./envs` lists a dir
```

The global store is **`~/.config/sandboxer/profiles/`** (override with
`$SANDBOXER_PROFILES`, or it follows `$XDG_CONFIG_HOME`). A bare positional that
matches a stored profile is used as that profile (its `name:` becomes the slug);
otherwise it stays a plain sandbox slug, so existing `create feat` usage is
unchanged. `-f`/`--config` works the same on `create`, `enter`, `exec` and
`show`.

A stored profile is used **whole** — there is no config-level inheritance or
merging between files. The effective precedence for a resolved setting is,
high → low:

```
flags  >  profile  >  SANDBOXER_* env  >  built-in
```

## Egress allowlist (container backend)

The agent sits on an `--internal` network with no direct outbound; its only exit
is an allowlist proxy that permits just the domains in `network.allowedDomains`
(everything else → 403). The proxy is a minimal **squid** sidecar (the
`sandboxer-proxy` image, built beside the toolbox image) running a generated
`squid.conf` — the `sandboxer` binary is **not** in the network path, and is not
baked into the toolbox image at all (it is a host tool). Disable with
`egress: false` in the profile or `SANDBOXER_NO_EGRESS=1`.

A single `network.proxy` URL routes outbound traffic; the egress toggle picks the
mode. With egress **on** the allowlist stays enforced and the sidecar chains
allowed traffic through the proxy (squid `cache_peer`, http:// only). With egress
**off** the agent talks to the proxy directly (`HTTP(S)_PROXY`); `network.noProxy`
is the direct-mode `NO_PROXY`. The env default is `SANDBOXER_PROXY`. A
`localhost`/`127.0.0.1` proxy is rewritten to the host gateway, so a proxy on your
host works with the obvious URL.

**Per-domain routes.** `network.routes` sends specific destination domains
through their own upstream proxy, overriding `network.proxy` for just those
domains — e.g. reach a geo-blocked API through a bypass proxy while everything
else stays direct (or on the default proxy):

```yaml
network:
  allowedDomains: [api.anthropic.com, registry.npmjs.org, github.com]
  proxy: http://corp:3128            # optional default parent for everything else
  routes:
    - domains: [api.anthropic.com]   # this one API bypasses the geo-block
      proxy: http://bypass-ru:8080
```

Each routed domain must also be in `allowedDomains` (squid denies it before the
route peer otherwise), a domain may route to only one proxy, and a routed peer
being down **fails closed** (503, never a leak). Routes need the allowlist on and
are ignored in direct mode (`egress: false`). Editing routes/proxy/domains now
recreates a running session automatically — the sidecar config is part of the
session's freshness.

## direnv

`sandboxer hook direnv` surfaces the **active** sandbox (the one set by
`sandboxer use <slug>`) to your host shell, so an `.envrc` — or any prompt /
editor that reads the environment — knows which sandbox is selected for the
project. It prints host-shell `export` lines for evaluation:

```sh
# .envrc
eval "$(sandboxer hook direnv)"
```

or, with the bundled [direnv](https://direnv.net) helper (copy
`contrib/direnv/use_sandboxer.sh` into `~/.config/direnv/direnvrc` once):

```sh
# .envrc
use sandboxer
```

`use sandboxer` also watches the active-sandbox marker (under the project's XDG
state dir), so direnv reloads the moment you switch the active sandbox with
`sandboxer use <slug>`.

What gets exported (only when a sandbox is active):

| var | source |
| --- | --- |
| `SANDBOXER_SLUG` | the active sandbox slug |
| `SANDBOXER_SRC` | the project root (absolute) |
| `SANDBOXER_BACKEND` | the recorded backend, if any |
| `SANDBOXER_ALLOW_DOMAINS` | the egress allowlist (csv), if any |

The hook is **read-only**: it prints already-persisted state and never builds or
starts anything — a `cd` into a project costs nothing. Outside a sandboxer
project, or with no active sandbox, it emits nothing (exit 0), so an `.envrc`
can call it unconditionally.

## Agents

```bash
sandboxer agents   # catalog: bin, sandbox mode, image inclusion, creds/env to bind
```

The registry is a single source of truth, `internal/registry/registry.json` (the
flake reads it too, to build the image). Adding an agent = one entry.

## Toolbox image

The container backend runs the agents inside the bundled `sandboxer-toolbox:latest`
image. Build it with **only docker/podman** — no nix on your machine:

```bash
sandboxer image build      # build + load the toolbox image (docker/podman only)
```

It drives an ephemeral, public `nixos/nix` container that builds a minimal OCI
image (agents from [llm-agents.nix](https://github.com/numtide/llm-agents.nix))
plus the small `sandboxer-proxy` (squid) egress image, and loads both into your
engine. The `sandboxer` binary is **not** baked in — it is a host tool. It is
clean by default — the builder container and the `nixos/nix` image it pulled are
removed afterward, leaving only the images; pass `--cache` to keep a nix-store
volume for faster rebuilds, `--keep-builder` to keep the `nixos/nix` image.

`create`/`enter`/`exec` **auto-build** the images on first use when missing
(disable with `SANDBOXER_NO_AUTOBUILD=1`) — the stock image and any `var-`
variant alike. Inspect or hand-run the container config with
`sandboxer compose <slug>` (or `--print-run`).

`sandboxer image build [profile]` (a positional name or `-f`, resolved like
enter/exec) builds that profile's customized variant instead of the stock
image. `--llm-agents-rev`/`--nixpkgs-rev` override the input revs for this one
build, on top of the profile's values; `--refresh` re-fetches the flake inputs
and re-resolves any `latest` pin (a `latest` rev resolves once and is stamped
into the pins cache — see
[Custom toolbox image](#custom-toolbox-image-image)). Any rev override selects
a `var-` tag too: rev flags **without** a profile pre-resolve the pins and
pre-build a variant, but the stock image profile-less sandboxes run is not
touched — only a profile pinning the same revs uses the result (the command
prints a note). Variant tags hash the input pins, so each sandboxer release (a
pin bump) rebuilds a variant once on first use; `--cache` keeps a nix-store
volume that makes those rebuilds cheap.

> Migrating from ≤0.20: tool-pack variants moved from `tools-*` to the unified
> `var-*` tags. The old `sandboxer-toolbox:tools-*` images are orphans —
> remove them with
> `podman images -q 'sandboxer-toolbox:tools-*' | xargs -r podman rmi`
> (same with `docker`).

```bash
nix run .#build-image   # maintainer/dev equivalent (requires nix on the host)
```

## Docs

- `sandboxer --help` / `sandboxer <cmd> --help` — commands, flags, examples
- [docs/architecture.md](./docs/architecture.md) — how it works: on-disk layout, lifecycle, image build, egress, registry
- [docs/troubleshooting.md](./docs/troubleshooting.md) — common problems, fixes, and FAQ
- [CONTRIBUTING.md](./CONTRIBUTING.md) — dev setup, Conventional Commits, releases
- [SECURITY.md](./SECURITY.md) — isolation model, vulnerability reporting
- [CHANGELOG.md](./CHANGELOG.md) — what's in each release

## Testing

```bash
go test ./...                                 # run all tests
go test ./... -cover                          # per-package coverage
go test ./... -coverprofile=cov.out           # write a profile
go tool cover -func=cov.out | tail -1         # total coverage
go tool cover -html=cov.out -o cov.html       # browseable report
```

CI enforces an engine-free **90% total coverage gate** on every push and PR
([ci.yml](./.github/workflows/ci.yml)); the badge above reflects that floor.
The container/e2e integration suite (`-tags integration`) runs separately —
see [CONTRIBUTING.md](./CONTRIBUTING.md#integration-tests).

Backend tests use fake engine/agent stubs on `PATH`, so they run without
containers or touching real credentials; the few tests that shell out (`git`,
real-engine integration) skip gracefully when the tool/engine is absent.

## License

MIT — see [LICENSE](./LICENSE).
