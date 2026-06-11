# sandboxer

[![CI](https://github.com/irasikhin/sandboxer/actions/workflows/ci.yml/badge.svg)](https://github.com/irasikhin/sandboxer/actions/workflows/ci.yml)
[![Coverage](https://img.shields.io/badge/coverage-92.2%25-brightgreen.svg)](#testing)
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

> ⚠️ **Pre-1.0.** CLI flags and the on-disk `.sandboxer/` layout may change
> between minor versions until 1.0. The **`.sandboxer/config.yaml` schema has settled**
> on `roots`+`deps` and is treated as stable through 0.x (the shipped
> `examples/` are CI-verified against the strict parser); any future change will
> be called out in the changelog.

## How it works

A **sandbox** is a directory under `.sandboxer/<slug>/` holding exactly the
`deps` you list — **nothing is copied by default**. The agent runs inside that
directory (isolated by the backend); the copies are pushed back to their origins
when you're done. No git is involved.

- **Sandbox** — the directory `.sandboxer/<slug>/`. It contains only what `deps`
  pulled in; no `deps` means an empty sandbox.
- **slug** — a short sandbox name (`feat`, `bugfix-auth`, …), set at `create`.
- **roots / deps** — the single source of a sandbox's contents: each `dep` is
  located by **path suffix** under the `roots` and copied flat to
  `<sandbox>/<dep>` (depsync-style).
- **pull / push** — `sandboxer pull` copies the `deps` in, keeping a target that
  already exists unless `--force`; `sandboxer push` copies them back over their
  origins (always overwriting, like depsync).

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
sandboxer init                            # scaffold a commented .sandboxer/config.yaml + image.nix to edit (optional)
sandboxer create feat                     # create a sandbox named "feat" (deps come from a profile)
sandboxer enter  feat                     # attach a shell (persistent session; Ctrl-q detaches)
sandboxer exec   feat -- claude           # run an agent/command inside it
sandboxer diff   feat                     # show what changed vs the deps' origins
sandboxer push   feat                     # copy the deps back to their origins
sandboxer stop   feat                     # park the session container (enter resumes it)
sandboxer list                            # status of all sandboxes (incl. session state)
sandboxer rm     feat                     # delete the sandbox and its session
```

To pull deps in, a profile must list them: drop a `.sandboxer/config.yaml` in the cwd
(auto-discovered), pass one with `-f` (a file, a directory of profiles, or a
[named profile](#named-profiles) from `~/.config/sandboxer/profiles/`), or refer
to a stored profile by name; the sandbox slug then comes from the profile's
`name:`.

Run a batch of autonomous agents — one sandbox per task:

```bash
sandboxer run tasks.txt --agent claude --max-parallel 4
# tasks file: [slug] sections + task text (see sandboxer.tasks.example)
```

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
| agent | `--agent` | `SANDBOXER_AGENT` (default `claude`) |
| backend | `--backend` | `SANDBOXER_BACKEND` (default `docker`; `docker\|podman` pins that engine when installed, else falls back to whichever is) |
| model | `--model` | `SANDBOXER_MODEL` |
| session mode | `--ephemeral` | `SANDBOXER_SESSION` (default `persistent`; the env wins over a profile's `session:`) |
| egress domains | `--allow-domains a,b` | `SANDBOXER_DOMAINS` |
| disable egress | — | `SANDBOXER_NO_EGRESS=1` |
| skip auto-scaffold | — | `SANDBOXER_NO_SCAFFOLD=1` (create/enter writes a default `.sandboxer/config.yaml` otherwise) |
| container engine | — | `SANDBOXER_ENGINE` (default: auto-detect docker→podman) |
| image | — | `SANDBOXER_IMAGE` (default `sandboxer-toolbox:latest`) |

Structured fields (`roots`/`deps`, `extraMounts`, `env`, `setup`, `tools`, `mcp`, `image`) live in an **optional**
`.sandboxer/config.yaml`. Point at it with `-f`/`--config`, which accepts a **file**, a
**directory** of profiles, or the **name** of a profile in the store (see
[Named profiles](#named-profiles)); with nothing given, a `.sandboxer/config.yaml` in
the cwd is auto-discovered. See `examples/config.yaml`,
`examples/with-deps.yaml` and `examples/profiles/`.

```yaml
name: feature-x
backend: docker
agent: claude
network:
  allowedDomains: [api.anthropic.com, registry.npmjs.org, github.com]
roots: [/abs/monorepo, /abs/shared]   # where to search
deps:
  - some_module          # any dir/file named some_module
  - src/lib/util.go      # any path ending with src/lib/util.go
setup: |                 # one-time prep, run once inside the sandbox
  npm ci
tools: [node, go]        # runtime tool packs baked into a per-profile image
mcp: [context7]          # MCP servers wired into the agent
```

`deps` are located by **path suffix** under `roots` and pulled into the sandbox
(`sandboxer pull`), copied flat to `<sandbox>/<dep>`. An already-present target
is kept unless `--force`. They are copied back to their origins (`sandboxer
push`), always overwriting. The copy preserves symlinks and file modes and
replaces the destination wholesale (depsync semantics).

> ⚠️ `push` (and the automatic copy-back after `enter`/`exec`) **overwrites each
> origin wholesale** — there is no merge and no signature check, so an
> out-of-band edit to an origin is lost. Run `sandboxer diff` first to see what
> will change. A `dep` that resolves to multiple paths uses the first match
> (the others are listed); absolute or `../` deps are refused.

`setup` is a one-time shell script (`bash -lc`) run inside the sandbox before
you take over — e.g. `npm ci`, a build, a DB seed. It runs on the first
`enter`/`exec`/`run` and again only when the script changes (a per-sandbox
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
[Custom toolbox image](#custom-toolbox-image-image)). `mcp` names MCP servers
(see `internal/registry/mcp.json`): the server config is seeded into the agent's
sandbox home (claude today) and each server's domains are folded into the egress
allowlist, so a sandboxed agent can use MCP without opening the network.

### Custom toolbox image (`image:`)

A profile can customize the toolbox image itself — extra packages, files, env,
even a nixpkgs overlay — **without nix on your machine** (the same builder
container does everything) and without forking the image. `sandboxer init`
scaffolds this section together with an inert, fully-commented
`image.nix` hook next to the profile under `.sandboxer/`, ready to edit:

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
never re-resolve; only `sandboxer build-image --refresh` moves it.

### Multiple profiles in one file

Instead of one profile per file, a `.sandboxer/config.yaml` can hold many under a
`profiles:` map. A shared **`defaults:`** block is auto-applied under every
profile (a profile's own fields win; `env` merges key-by-key). To inherit from
*another profile*, use plain **YAML anchors** — anchor one (`&api`) and merge it
into another (`<<: *api`); no special key is needed, and the node's own fields
win over the merge. `default:` names the one used when you don't name a section.
The flat one-profile form above still works. See `examples/multi-profile.yaml`.

```yaml
defaults:
  agent: claude
  network:
    allowedDomains: [api.anthropic.com, github.com]

profiles:
  api: &api                # sandboxer create api
    backend: docker
    model: opus
    deps: [shared/proto]
  api-prod:                # sandboxer create api-prod
    <<: *api               # inherit api (anchor) + defaults, then override
    env: { NODE_ENV: production }

default: api               # sandboxer create   (no name → api)
```

`sandboxer create <name>` selects the section by name (that name becomes the
sandbox slug); a name that matches no section falls back to the store / a plain
slug. A batch `sandboxer run` uses the `default:` (or sole) profile.

### Named profiles

Keep reusable profiles as files and select them by **name** instead of by path.
A profile's name is its file's base name (`web.yaml` → `web`) unless the file
sets an explicit `name:`, which wins. There are three sources, in precedence
order:

```bash
sandboxer create ./feat.yaml          # an explicit file
sandboxer create api  -f ./envs        # the profile named "api" inside a directory
sandboxer create web                   # a named profile from the global store
sandboxer profiles                     # list the store; `profiles -f ./envs` lists a dir
```

The global store is **`~/.config/sandboxer/profiles/`** (override with
`$SANDBOXER_PROFILES`, or it follows `$XDG_CONFIG_HOME`). A bare positional that
matches a stored profile is used as that profile (its `name:` becomes the slug);
otherwise it stays a plain sandbox slug, so existing `create feat` usage is
unchanged. `-f`/`--config` works the same on `create`, `enter`, `exec`, `show`
and `run`.

### Global config

A **global config** lets you set defaults once for every project. It is a full
document — a `defaults:` block plus an optional `profiles:` map, the same shape
as a project `.sandboxer/config.yaml` — read from the first of:

```
$SANDBOXER_CONFIG                          # explicit override
$XDG_CONFIG_HOME/sandboxer/config.yaml
~/.config/sandboxer/config.yaml            # the default
```

It is **optional**: an absent file is a clean no-op. When present, its
**`defaults:` merge UNDER the project's** — the project always wins. The
effective precedence for a resolved profile is, high → low:

```
flags  >  project profile section  >  project defaults  >  GLOBAL defaults  >  SANDBOXER_* env  >  built-in
```

The merge is per field (`env` merges key-by-key, `image:` per field), so the
global can pin the image revisions (`image.llmAgentsRev` / `image.nixpkgsRev`)
while a project adds its own `image.extraPkgs` and both apply. A profile **name**
resolves project → global → store: a name not found in the project config falls
back to a `profiles:` entry in the global config, then to the named-profile
store above.

```yaml
# ~/.config/sandboxer/config.yaml
defaults:
  agent: claude                 # every project's default agent
  image:
    llmAgentsRev: <40-hex>      # pin the toolbox flake inputs org-wide
    nixpkgsRev:   <40-hex>
```

> **Keep `roots:`/`deps:` out of the global config.** They are project-specific
> (deps are located by path suffix under your roots, per project), so a global
> `defaults.roots`/`defaults.deps` is a foot-gun — it would copy the wrong tree
> into every sandbox. Put roots/deps in the project `.sandboxer/config.yaml`.

## Egress allowlist (container backend)

The agent sits on an `--internal` network with no direct outbound; its only exit
is an allowlist proxy that permits just the domains in `network.allowedDomains`
(everything else → 403). The proxy is **the same binary** in a hidden
`sandboxer _proxy` mode (no external dependency). Disable with `egress: false`
in the profile or `SANDBOXER_NO_EGRESS=1`. A configured upstream proxy holds the
boundary itself.

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

`use sandboxer` also `watch_file .sandboxer/_meta/current`, so direnv reloads
the moment you switch the active sandbox with `sandboxer use <slug>`.

What gets exported (only when a sandbox is active):

| var | source |
| --- | --- |
| `SANDBOXER_SLUG` | the active sandbox slug |
| `SANDBOXER_SRC` | the project root (absolute) |
| `SANDBOXER_MODEL` | the recorded model, if any |
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
sandboxer build-image      # build + load the toolbox image (docker/podman only)
```

It drives an ephemeral, public `nixos/nix` container that builds a minimal OCI
image (agents from [llm-agents.nix](https://github.com/numtide/llm-agents.nix);
the sandboxer binary injected by copy) and loads it into your engine. It is clean
by default — the builder container and the `nixos/nix` image it pulled are removed
afterward, leaving only the toolbox image; pass `--cache` to keep a nix-store
volume for faster rebuilds, `--keep-builder` to keep the `nixos/nix` image.

`create`/`enter`/`exec`/`run` **auto-build** the image on first use when it's
missing (disable with `SANDBOXER_NO_AUTOBUILD=1`) — the stock image and any
`var-` variant alike. Inspect or hand-run the container config with
`sandboxer compose <slug>` (or `--print-run`).

`sandboxer build-image [profile]` (a positional name or `-f`, resolved like
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

Current coverage: **92.2%** total. Per package:

| Package             | Coverage |
| ------------------- | -------- |
| `cmd/sandboxer`     | 100.0%   |
| `internal/config`   | 99.0%    |
| `internal/backend`  | 96.0%    |
| `internal/runner`   | 95.6%    |
| `internal/egress`   | 94.3%    |
| `internal/registry` | 94.1%    |
| `internal/cli`      | 90.9%    |
| `internal/proxy`    | 90.3%    |
| `internal/srcs`     | 87.0%    |
| `internal/sandbox`  | 88.8%    |

Backend tests use fake engine/agent stubs on `PATH`, so they run without
containers or touching real credentials; the few tests that shell out (`diff`,
real-engine integration) skip gracefully when the tool/engine is absent.

## License

MIT — see [LICENSE](./LICENSE).
