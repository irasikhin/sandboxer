# sandboxer

[![CI](https://github.com/irasikhin/sandboxer/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/irasikhin/sandboxer/actions/workflows/ci.yml?query=branch%3Amain)
[![Coverage](https://img.shields.io/badge/coverage-92.2%25-brightgreen.svg)](#testing)
[![Go](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Run **several autonomous coding agents in parallel** — or work by hand in a
single sandbox — each fully isolated, on your **local Linux machine**. A Go CLI,
shipped as a static binary, `go install`, or a Nix flake. Human designs, AI
drives.

> ⚠️ **Pre-1.0.** CLI flags and the on-disk `.sandboxer/` layout may change
> between minor versions until 1.0. The **`.sandboxer.yaml` schema has settled**
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

Isolation backend — a **podman / docker** container built from a toolbox image
with the agents baked in (claude, opencode, crush, aider, pi, gemini). Any of
them; each sandbox gets its own isolated home, and network, proxy and
credentials are wired per config.

## Install

Linux only. The container engine (podman or docker) is **not bundled** — it
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
sandboxer init                            # scaffold a commented .sandboxer.yaml to edit (optional)
sandboxer create feat                     # create a sandbox named "feat" (deps come from a profile)
sandboxer enter  feat                     # interactive shell inside it (agents on PATH)
sandboxer exec   feat -- claude           # run an agent/command inside it
sandboxer diff   feat                     # show what changed vs the deps' origins
sandboxer push   feat                     # copy the deps back to their origins
sandboxer list                            # status of all sandboxes
sandboxer rm     feat                     # delete the sandbox
```

To pull deps in, a profile must list them: drop a `.sandboxer.yaml` in the cwd
(auto-discovered), pass one with `-f` (a file, a directory of profiles, or a
[named profile](#named-profiles) from `~/.config/sandboxer/profiles/`), or refer
to a stored profile by name; the sandbox slug then comes from the profile's
`name:`.

Run a batch of autonomous agents — one sandbox per task:

```bash
sandboxer run tasks.txt --agent claude --max-parallel 4
# tasks file: [slug] sections + task text (see sandboxer.tasks.example)
```

## Config

Scalars come from **flags** and `SANDBOXER_*` env vars:

| Setting | Flag | Env |
|---------|------|-----|
| agent | `--agent` | `SANDBOXER_AGENT` (default `claude`) |
| backend | `--backend` | `SANDBOXER_BACKEND` (default `podman`; `podman\|docker` pins that engine when installed, else falls back to whichever is) |
| model | `--model` | `SANDBOXER_MODEL` |
| egress domains | `--allow-domains a,b` | `SANDBOXER_DOMAINS` |
| disable egress | — | `SANDBOXER_NO_EGRESS=1` |
| skip auto-scaffold | — | `SANDBOXER_NO_SCAFFOLD=1` (create/enter writes a default `.sandboxer.yaml` otherwise) |
| container engine | — | `SANDBOXER_ENGINE` (default: auto-detect podman→docker) |
| image | — | `SANDBOXER_IMAGE` (default `sandboxer-toolbox:latest`) |

Structured fields (`roots`/`deps`, `extraMounts`, `env`, `setup`) live in an **optional**
`.sandboxer.yaml`. Point at it with `-f`/`--config`, which accepts a **file**, a
**directory** of profiles, or the **name** of a profile in the store (see
[Named profiles](#named-profiles)); with nothing given, a `.sandboxer.yaml` in
the cwd is auto-discovered. See `examples/.sandboxer.yaml`,
`examples/with-deps.yaml` and `examples/profiles/`.

```yaml
name: feature-x
backend: podman
agent: claude
network:
  allowedDomains: [api.anthropic.com, registry.npmjs.org, github.com]
roots: [/abs/monorepo, /abs/shared]   # where to search
deps:
  - some_module          # any dir/file named some_module
  - src/lib/util.go      # any path ending with src/lib/util.go
setup: |                 # one-time prep, run once inside the sandbox
  npm ci
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

### Multiple profiles in one file

Instead of one profile per file, a `.sandboxer.yaml` can hold many under a
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

## Egress allowlist (container backend)

The agent sits on an `--internal` network with no direct outbound; its only exit
is an allowlist proxy that permits just the domains in `network.allowedDomains`
(everything else → 403). The proxy is **the same binary** in a hidden
`sandboxer _proxy` mode (no external dependency). Disable with `egress: false`
in the profile or `SANDBOXER_NO_EGRESS=1`. A configured upstream proxy holds the
boundary itself.

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
missing (disable with `SANDBOXER_NO_AUTOBUILD=1`). Inspect or hand-run the
container config with `sandboxer compose <slug>` (or `--print-run`).

```bash
nix run .#build-image   # maintainer/dev equivalent (requires nix on the host)
```

## Docs

- `sandboxer --help` / `sandboxer <cmd> --help` — commands, flags, examples
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
