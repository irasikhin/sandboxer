# sandboxer

[![CI](https://github.com/irasikhin/sandboxer/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/irasikhin/sandboxer/actions/workflows/ci.yml?query=branch%3Amain)
[![Coverage](https://img.shields.io/badge/coverage-91.9%25-brightgreen.svg)](#testing)
[![Go](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Run **several autonomous coding agents in parallel** — or work by hand in a
single sandbox — each fully isolated, on your **local Linux machine**. A Go CLI,
shipped as a static binary, `go install`, or a Nix flake. Human designs, AI
drives.

> ⚠️ **Pre-1.0.** CLI flags, the config schema, and the on-disk `.sandboxer/`
> layout may change between minor versions until 1.0.

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

Two isolation backends:

- **native** — Claude Code's own `/sandbox` (bubblewrap; OS-level FS + network).
  `claude` only, zero install.
- **podman / docker** (default) — a toolbox container with the agents baked in
  (claude, opencode, crush, aider, pi, gemini). Any of them; network, proxy and
  credentials are wired per config.

## Install

Linux only. `claude` (for native) and the container engine are **not bundled** —
they come from the host.

```bash
nix run    github:irasikhin/sandboxer -- help                   # try without installing
nix profile install github:irasikhin/sandboxer                  # Nix
go install github.com/irasikhin/sandboxer/cmd/sandboxer@latest  # Go
```

Or grab a [pre-built binary](https://github.com/irasikhin/sandboxer/releases)
(linux amd64/arm64).

## Quick start

```bash
sandboxer create --config sandboxer.yaml  # create a sandbox, pull its deps in
sandboxer enter  feat                     # interactive shell inside it (agents on PATH)
sandboxer exec   feat -- claude           # run an agent/command inside it
sandboxer diff   feat                     # show what changed vs the deps' origins
sandboxer push   feat                     # copy the deps back to their origins
sandboxer list                            # status of all sandboxes
sandboxer rm     feat                     # delete the sandbox
```

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
| backend | `--backend` | `SANDBOXER_BACKEND` (default `podman`) |
| model | `--model` | `SANDBOXER_MODEL` |
| egress domains | `--allow-domains a,b` | `SANDBOXER_DOMAINS` |
| image | — | `SANDBOXER_IMAGE` (default `sandboxer-toolbox:latest`) |

Structured fields (`roots`/`deps`, `extraMounts`, `env`) live in an **optional**
`sandboxer.yaml` (auto-discovered in the cwd, or `--config <file>`). See
`examples/sandboxer.yaml` and `examples/with-deps.yaml`.

```yaml
name: feature-x
backend: native
agent: claude
network:
  allowedDomains: [api.anthropic.com, registry.npmjs.org, github.com]
roots: [/abs/monorepo, /abs/shared]   # where to search
deps:
  - some_module          # any dir/file named some_module
  - src/lib/util.go      # any path ending with src/lib/util.go
```

`deps` are located by **path suffix** under `roots` and pulled into the sandbox
(`sandboxer pull`), copied flat to `<sandbox>/<dep>`. An already-present target
is kept unless `--force`. They are copied back to their origins (`sandboxer
push`), always overwriting. The copy preserves symlinks and file modes and
replaces the destination wholesale (depsync semantics).

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

```bash
nix run .#build-image   # build an OCI image with the agents + binary, load into podman/docker
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

Current coverage: **91.9%** total. Per package:

| Package             | Coverage |
| ------------------- | -------- |
| `cmd/sandboxer`     | 100.0%   |
| `internal/config`   | 100.0%   |
| `internal/backend`  | 94.9%    |
| `internal/runner`   | 94.7%    |
| `internal/egress`   | 94.3%    |
| `internal/registry` | 94.1%    |
| `internal/cli`      | 91.3%    |
| `internal/proxy`    | 90.3%    |
| `internal/srcs`     | 87.2%    |
| `internal/sandbox`  | 88.8%    |

Backend tests use fake engine/agent stubs on `PATH`, so they run without
containers or touching real credentials; the few tests that shell out (native
`enter`/`exec`, `diff`) skip gracefully when the tool is absent.

## License

MIT — see [LICENSE](./LICENSE).
