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

> ⚠️ **Pre-1.0.** CLI flags, the `sandboxer.nix` schema and the on-disk layout may change
> between minor versions until 1.0. Sandboxes expose **sources** — git repos
> checked out into host-side worktrees, narrowed by `srcs` include patterns
> (see below). Any future change will be called out in the changelog.

## How it works

A **sandbox** exposes **sources**: git repos checked out into per-sandbox
worktrees right in the project — `./sandboxes/<slug>/<repo>/<branch>`,
grouped by repo with each worktree directory named after its branch, so your
sandboxes are ordinary folders you can browse. The dir is auto-added to the
project's `.gitignore` (worktrees never land in a commit); relocate it with
the profile's `worktreesDir` (absolute, `~`, or project-relative). Run sandboxer inside a repo and it just works — the
scaffolded config pins the whole repo as the one source, on an explicit
branch (always explicit, never implied). The
container sees **only the files the sources select — git itself never enters
it**: no `.git` mounts, no history, no hooks. The agent edits files; the edits
land live in the host-side worktree, where you review and commit them with
plain git. Your working tree and current branch are never touched, and nothing
is copied.

- **Sandbox** — a set of sources materialized as git worktrees under one dir.
- **slug** — a short sandbox name (`feat`, `bugfix-auth`, …), set at `create`.
- **srcs** — the sources, always explicit: each entry is `src:` (path to a
  repo root — relative to the project root, so other repos work too), a
  REQUIRED `branch:` (the branch the worktree lives on — it also names the
  worktree's directory; a branch already checked out elsewhere is adopted
  as-is) and an optional `include:` (gitignore-style patterns — only matching
  files exist in the sandbox). The scaffold seeds
  `srcs = [ { src = "."; branch = "feat/<name>"; } ]` — this repo, whole.
  Editing srcs applies on the next `enter`/`exec` — a running session sees the
  change live, no recreate.
- **review** — on the HOST, per source repo: `git -C <repo> log <branch>`,
  `git add`/`commit` in the worktree, then merge or cherry-pick.

sandboxer is **git-only**: every `src` must be a git repo with at least one
commit (`git init && git add -A && git commit -m init`). Non-git trees come in
via `extraMounts`.

Isolation backend — a **docker / podman** container built from a toolbox image
with the agents baked in (claude, opencode, crush, aider, pi, gemini) plus an
everyday toolchain: python3, node/npm, jdk+maven, redocly, ripgrep/fd/jq/…,
tmux (auto-attached by `enter`), and **rootless podman** for nested containers (never
docker-in-docker — no engine socket is ever mounted; pulls go through the
egress allowlist, whose defaults include docker.io/ghcr.io/quay.io and
mirror.gcr.io). Each sandbox gets its own isolated home, and network/proxy
are wired per config. Agent auth is yours to choose: with `hostConfigs = true`
(the scaffolded default) the sandbox home is seeded with a COPY of your host
agent configs — `~/.claude` (settings, skills, memory) + `~/.claude.json`,
`~/.codex`, `~/.gemini`, opencode/crush/aider — transcripts/caches excluded,
never mounted, never written back, and your in-sandbox edits always win; the
agents' auth env vars set on the host (`ANTHROPIC_API_KEY`,
`CLAUDE_CODE_OAUTH_TOKEN`, …) are passed through as well. Claude's rotating
OAuth file is deliberately not copied — a copy goes 401 on the next refresh
either side performs: for subscription auth run `claude setup-token` once and
export `CLAUDE_CODE_OAUTH_TOKEN`, or `/login` once inside the sandbox (its
private $HOME persists). Without `hostConfigs`, credentials never come from
the host at all.

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
sandboxer config init                     # scaffold a commented sandboxer.nix to edit (optional)
sandboxer create feat                     # create a sandbox named "feat" (worktree on branch feat/feat)
sandboxer enter  feat                     # attach a shell (persistent session; Ctrl-q detaches)
sandboxer exec   feat -- claude           # run an agent/command inside it
git log feat/feat                      # the work is an ordinary branch (commit it on the host)
sandboxer stop   feat                     # park the session container (enter resumes it)
sandboxer list                            # status of all sandboxes (alias: sandboxer status)
sandboxer rm     feat                     # delete the sandbox and its session (keeps the branch)
```

A profile is optional (empty = the whole repo). To narrow the sandbox or add
setup/tools/env, edit the `sandboxer.nix` in the cwd (auto-discovered) or pass
another file with `-f`; several profiles live in ONE file under a `profiles`
attrset and are picked by name (`create <name>`). The sandbox slug comes from
the profile's `name`.

Commands group into three activities (also shown that way in `--help`): forming
the **image** (`sandboxer image build|rm`) and **config**
(`sandboxer config init|edit|validate`, plus
`sandboxer profile list|use` for picking one), managing **state** (`clean`),
and **entering/working** in the sandbox
(`create` / `enter` / `exec` / `stop` / `reset` / `rm` / `list` / `use`).

## Config vs data

The committed config is ONE file at your repo root — `sandboxer.nix`,
image-customization hook included — checked in as-is. It is a nix attrset,
evaluated by the **host nix** under a restricted eval (no network, no reads
outside its directory); nix on the host is a hard requirement. The sandbox worktrees live in the
project's `./sandboxes/` (auto-git-ignored; relocatable via the profile's
`worktreesDir`); the rest of the runtime state (the private agent homes, logs
and metadata) lives under the XDG state dir
(`$XDG_STATE_HOME/sandboxer/<project>`, default `~/.local/state/...`), outside
the repo. Secrets and scratch data can never be committed. `sandboxer clean`
wipes both for the project (the config stays; a user-chosen worktrees dir is
never removed wholesale — only the sandbox dirs in it); a dropped source's
worktree is removed when clean (its commits live on the branch, which is
kept), and set aside under `_detached/` only when it holds uncommitted work —
sweep those with `sandboxer clean --detached --force` once reviewed, or name
the branch in `srcs` again and the worktree is re-attached, work intact.
Deleting `./sandboxes/` by hand is fine too: the next `enter` prunes the stale
git registrations, checks the branches out fresh and rebuilds the session
container (uncommitted work is the only thing an `rm -rf` forfeits).

## How changes flow

Changes flow through git — on the host. Each source is a **git worktree** on
the branch you configured (`srcs branch:`); the container's edits appear
there live (bind mount), and **you** commit/review them with plain git
(`git -C <worktree> add/commit`, `git log`/`git diff`/`git merge <branch>`).
The container itself has no git access at all: no object store, no hooks, no
history. There is no copy-in and no push-back. Teardown (`rm`, `recreate`)
keeps the branches; `recreate --full` also deletes the branches sandboxer
itself created, for a fresh start (never one that existed before the
sandbox).

### Continuing after a merged PR

When a sandbox's branch has been merged and its remote branch deleted, re-base
the same branch onto the freshly-merged default:

```bash
sandboxer reset feat                   # fetch + move every source's branch onto origin/main
sandboxer reset feat api               # just the "api" source
sandboxer reset feat --onto origin/master   # a repo whose default differs
git -C "$(sandboxer path feat)" push -u origin HEAD   # re-create the remote branch for the next PR
```

`reset` stays ON the sandbox branch (never a `git checkout main`, so the base
branch checked out in your main repo is never contended), refuses a source
with uncommitted work unless `--force` (checked across the whole sandbox
BEFORE anything moves — never a half-reset), and skips adopted worktrees. A
live session sees the new base immediately (the worktree is a bind mount), so
no `recreate` is needed. It is plain `git fetch` + `reset --hard` under the
hood — the manual equivalent is always available via `sandboxer path`.

## Persistent sessions

By default `enter` attaches a **tmux session inside a persistent session
container** (`tmux -L sandboxer`, mouse scrolling on, sandboxer prompt in
every pane): `Ctrl-b d` detaches, exiting keeps the container running, and a
later `sandboxer enter feat` drops straight back in; a second terminal
attaches the same session in parallel (`--session <name>` opens a separate
one in the same container). `exec` reuses a running session;
`stop` parks the container for a later resume; `rm` removes it along with the
sandbox. `list`'s STATE column shows `running`/`stopped`/`-` per sandbox. When
the profile changes or the toolbox image is rebuilt, the next `enter`
recreates the session (announced — anything still running inside is gone, so
finish or park long jobs first).

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
| skip auto-scaffold | — | `SANDBOXER_NO_SCAFFOLD=1` (create/enter writes a default `sandboxer.nix` otherwise) |
| container engine | — | `SANDBOXER_ENGINE` (default: auto-detect docker→podman) |
| container user | — | `SANDBOXER_CONTAINER_USER` (default: host uid:gid; empty omits `--user` — macOS escape hatch, see [docs/macos.md](docs/macos.md)) |
| image | — | `SANDBOXER_IMAGE` (default `sandboxer-toolbox:latest`) |
| resource caps | — | `SANDBOXER_MEM` / `SANDBOXER_CPU` (or the profile's `limits:` — see below) |

The sandbox container's resource caps come from the profile's `limits:` block
(`memory`, `cpus`, `pids`), overriding the `SANDBOXER_MEM`/`SANDBOXER_CPU` env
defaults; `pids` (a `--pids-limit`, bounding fork-bomb blast radius) is
profile-only. Empty means uncapped.

Structured fields (`srcs`, `extraMounts`, `env`, `setup`, `tools`, `image`, `limits`) live in an **optional**
`sandboxer.nix`. With nothing given, the `sandboxer.nix` in the cwd is
auto-discovered; `-f`/`--config` points at another config **file**. Several
profiles live in one file under a `profiles` attrset. See `examples/config.nix`,
`examples/with-srcs.nix` and `examples/multi-profile.nix`.

> `sandboxer.nix` is meant to be **committed** with your repo — don't
> gitignore it (`sandboxer doctor` warns when a rule hides it).

```nix
{
  name = "feature-x";
  backend = "docker";
  egress.allowedDomains = [ "api.anthropic.com" "registry.npmjs.org" "github.com" ];
  srcs = [                  # the sources the sandbox sees (this repo, narrowed)
    { src = "."; include = [ "/src/lib/" "/shared/proto/" ]; }
  ];
  setup = ''                # one-time prep, run once inside the sandbox
    npm ci
  '';
  tools = [ "node" "go" ];  # runtime tool packs baked into a per-profile image
}
```

Each `srcs` entry is a repo (`src` — `.` or a path to another repo's root)
narrowed by `include` — **a list of directories** (`/src/proto/`, `/shared/lib/`;
anchored at the repo root, slash-terminated; omit for the whole repo) — and
optionally pinned with `branch` — naming a branch whose worktree already
exists (even your main checkout) **adopts** it instead of creating one. Editing
`srcs` applies on the next `enter`/`exec` and is visible to a **running**
session immediately. To bring in **non-git** trees, use `extraMounts`.

`include` narrows **what the container sees, and nothing else**: the host's
worktree is always a complete checkout, so your IDE opens the branch and
indexes it normally. The narrowing is enforced by bind-mounting only the listed
directories into the container — what is not listed is not mounted, and
therefore does not exist inside. This is why `include` takes directories rather
than gitignore patterns: a glob (`*.md`, `!/vendor/`) selects a file *set*,
which a mount cannot express — mounting files one by one would break atomic
saves (write-temp + rename over a mountpoint fails). A glob is refused by
`sandboxer config validate` with the directory form to use instead.

`setup` is a one-time shell script (`bash -lc`) run inside the sandbox before
you take over — e.g. `npm ci`, a build, a DB seed. It runs on the first
`enter`/`exec` and again only when the script changes (a per-sandbox
stamp tracks it), under the **same egress allowlist** as the sandbox (so a
network install needs its domains allowed). A failed setup is fatal by default;
skip it with `--no-setup`. The baked shell can also be extended without
rebuilding the image: drop `*.sh` files in `/etc/sandboxer/rc.d/` (image
plugins) or write `~/.config/sandboxer/rc` (per-sandbox `$HOME`).

> ⚠️ The config itself **evaluates on your host** (restricted: no network, no
> reads outside its directory), and `setup` / the image `overlay` run
> **arbitrary code** — setup inside the sandbox under its egress allowlist,
> the overlay inside the throwaway image-builder container with full network. That is the
> intended trust level for *your own* configs; treat a third-party
> sandboxer.nix like a shell script someone sent you — read it first.

`tools` names language/runtime packs (`node`, `python`, `go`, `rust`, … — see
`internal/registry/tools.json`) baked into a **per-profile toolbox image**
variant, built on demand and content-addressed (see
[Custom toolbox image](#custom-toolbox-image-image)).

MCP servers need no sandboxer wiring: the sandbox contains your repo's files,
so agent-level MCP config committed there (e.g. a `.mcp.json`) works as-is —
just add the servers' domains to `egress.allowedDomains`.

### Editing the config

The file is the whole interface: `sandboxer config edit` opens it in `$EDITOR`
(scaffolding the commented starter first if missing), `sandboxer config
validate` evaluates it and checks the schema strictly — an unknown attr or a
retired key fails with a precise message. Existing sandboxes pick changes up
on their next `enter`/`exec` — `srcs` included: even a running session sees
new sources live.

### Custom toolbox image (`image:`)

A profile can customize the toolbox image itself — without forking it (the
build runs inside a builder container; host nix only evaluates the config).
Everything is **flat data** in `image`; the one thing that needs `pkgs` at
build time (an overlay) is a separate file:

```nix
{
  image = {
    packages = [ "gh" "python3Packages.requests" ];  # nixpkgs attr names baked in
    files."/etc/sandboxer/rc.d/10-aliases.sh" = "alias mci='mvn clean install'";
    env.SANDBOX_FLAVOR = "custom";                    # static image OCI env
    # overlay = "./overlay.nix";  # a PLAIN nixpkgs overlay, for computed pkgs
    # llmAgentsRev = "latest";    # input pin override: latest | full commit hash
    # nixpkgsRev = "<commit>";    # empty = the pin embedded in the binary
  };
}
```

- **`packages`** — nixpkgs attribute names (dotted paths like
  `python3Packages.requests` allowed). They resolve against the overlaid
  package set, so an attr your overlay defines is listed here like any other.
- **`files`** — static text at absolute in-image paths (shell drop-ins under
  `/etc/sandboxer/rc.d/*.sh` are sourced by every interactive shell).
- **`env`** — static additions to the image's OCI env (the profile's own
  top-level `env` still overrides at run time).
- **`overlay`** — a file with a **plain nixpkgs overlay**, `final: prev: { … }`,
  for anything that needs `pkgs` at build time (patched or computed packages).
  Expose those as overlay attrs and name them in `packages`:

  ```nix
  # overlay.nix
  final: prev: {
    greet = prev.writeShellScriptBin "greet" "echo hi";
  }
  ```

The customization is **content-addressed**: the sandbox runs
`sandboxer-toolbox:var-<12hex>` — hashed over the effective input pins, the
package set (`tools` packs + `packages`), `files`, `env` and the overlay's
content — auto-built on first use and shared by identical profiles; the stock
`sandboxer-toolbox:latest` is untouched. Any change is a new tag, and an idle
persistent session recreates itself on the next `enter`. Full commented
example: [examples/custom-image.nix](./examples/custom-image.nix).

`llmAgentsRev`/`nixpkgsRev` move the image's flake-input pins — e.g. pick up
newer agents without waiting for a sandboxer release. A full 40-hex commit
hash pins exactly; `latest` is resolved to the remote head **once**, inside the
builder container at build time, and stamped into the per-user pins cache
(`~/.cache/sandboxer/image-pins.json`). `enter`/`exec` reuse the stamp and
never re-resolve; only `sandboxer image build --refresh` moves it.

### Nested containers (`nestedContainers`)

The toolbox image ships a **rootless podman**, so a sandbox can build and run
containers of its own. It is **off by default**, because switching it on costs
isolation — turn it on per profile:

```nix
{
  nestedContainers = true;   # the sandbox may run its own rootless podman
}
```

No engine socket is ever mounted (this is not docker-in-docker): the sandbox
gets its own podman, and its pulls ride the sandbox's `HTTP(S)_PROXY` through
the **egress allowlist** like any other traffic — allow the registry's domains
or the pull is refused.

The cost is real: podman re-execs itself into a user namespace, and the engine's
default seccomp profile denies that to an unprivileged container, so the opt-in
runs the sandbox with **`seccomp=unconfined` and `/proc` unmasked** (plus
`/dev/net/tun` and `/dev/fuse`). It does *not* hand over privilege — no
`--privileged`, no `--cap-add`, `--cap-drop=ALL` and `no-new-privileges` stay —
but the syscall filter is gone. See [SECURITY.md](./SECURITY.md).

Because there is no subordinate uid range inside, the nested podman maps a
single uid; the image's `storage.conf` sets `ignore_chown_errors` so ordinary
images still unpack.

### Multiple profiles in one file

Instead of one profile per file, a `sandboxer.nix` can hold many under a
`profiles` attrset. Every section is **self-contained** — there is no shared
defaults layer and no merging between files. Reuse between sections is
ordinary nix — a `let`-bound base merged in with `//` (the section's own attrs
win). `default` names the one used when you don't name a section. The flat
one-profile form above still works. See `examples/multi-profile.nix`.

```nix
let
  api = {
    backend = "docker";
    egress.allowedDomains = [ "api.anthropic.com" "github.com" ];
    srcs = [ { src = "."; include = [ "/shared/proto/" ]; } ];
  };
in {
  profiles = {
    inherit api;                                  # sandboxer create api
    api-prod = api // { env.NODE_ENV = "production"; };  # sandboxer create api-prod
  };
  default = "api";                                # sandboxer create  (no name → api)
}
```

`sandboxer create <name>` selects the section by name (that name becomes the
sandbox slug); a name that matches no section stays a plain slug. With no
name, `create` uses the `default` (or sole) profile. An explicit file also
works: `sandboxer create ./feat.nix` or `-f other.nix`; `sandboxer profile
list` shows the file's sections.

## Egress allowlist (container backend)

The agent sits on an `--internal` network with no direct outbound; its only exit
is an allowlist proxy that permits just the domains in `egress.allowedDomains`
(everything else → 403). The proxy is a minimal **squid** sidecar (the
`sandboxer-proxy` image, built beside the toolbox image) running a generated
`squid.conf` — the `sandboxer` binary is **not** in the network path, and is not
baked into the toolbox image at all (it is a host tool). Disable with
`egress.enabled = false` in the profile or `SANDBOXER_NO_EGRESS=1`.

A single `egress.proxy` URL routes outbound traffic; `egress.enabled` picks the
model. With the allowlist **on** (the default) it stays enforced and the sidecar
chains allowed traffic through the proxy (squid `cache_peer`, http:// only). With
`egress.enabled = false` (direct mode) the agent talks to the proxy directly
(`HTTP(S)_PROXY`) and that proxy is trusted to police egress; `egress.noProxy` is
the direct-mode `NO_PROXY`. The env default is `SANDBOXER_PROXY`. A
`localhost`/`127.0.0.1` proxy is rewritten to the host gateway, so a proxy on your
host works with the obvious URL.

**Per-domain routes.** `egress.routes` sends specific destination domains
through their own upstream proxy, overriding `egress.proxy` for just those
domains — e.g. reach a geo-blocked API through a bypass proxy while everything
else stays direct (or on the default proxy):

```nix
{
  egress = {
    allowedDomains = [ "api.anthropic.com" "registry.npmjs.org" "github.com" ];
    proxy = "http://corp:3128";        # optional default parent for everything else
    routes = [
      { domains = [ "api.anthropic.com" ]; proxy = "http://bypass-ru:8080"; }
    ];
  };
}
```

Each routed domain must also be in `allowedDomains` (squid denies it before the
route peer otherwise), a domain may route to only one proxy, and a routed peer
being down **fails closed** (503, never a leak). Routes need the allowlist on and
are ignored in direct mode (`egress.enabled = false`). Editing routes/proxy/domains now
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
sandboxer agents   # catalog: bin, image inclusion, auth env vars (set them INSIDE)
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
