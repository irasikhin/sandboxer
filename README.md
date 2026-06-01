# sandboxer

[![CI](https://github.com/irasikhin/sandboxer/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/irasikhin/sandboxer/actions/workflows/ci.yml?query=branch%3Amain)
[![Coverage](https://img.shields.io/badge/coverage-91.8%25-brightgreen.svg)](#testing)
[![Go](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Run **several autonomous coding agents in parallel** — or work by hand in a
single sandbox — each fully isolated, on your **local Linux machine**. A Go CLI,
shipped as a static binary, `go install`, or a Nix flake. Human designs, AI
drives.

> ⚠️ **Pre-1.0.** CLI flags, the config schema, and the on-disk `.sandboxer/`
> layout may change between minor versions until 1.0.

## How it works

A **sandbox** is an isolated **directory copy** of your project (rsync of
`mainSrc`) under `.sandboxer/<slug>/`. The agent runs only inside that copy —
your project is untouched until you explicitly return the work.

- **Sandbox** — the project copy at `.sandboxer/<slug>/` (`.git`, `node_modules`
  and `.sandboxer` are excluded from the copy).
- **slug** — a short sandbox name (`feat`, `bugfix-auth`, …), set at `create`.
- **Baseline** — file signatures recorded at create time, so sandboxer knows
  exactly which files the agent changed.
- **Return** (`sandboxer return <slug>`) — copies the changed files back over
  the source project. A source file edited after create is skipped unless
  `--force`. This is the only moment your project changes; review afterwards
  with your own tools (`git diff` in your repo, etc.).

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
sandboxer create feat            # create sandbox feat (copy under .sandboxer/feat/)
sandboxer enter  feat            # interactive shell inside it (agents on PATH)
sandboxer exec   feat -- claude  # run an agent/command inside it
sandboxer diff   feat            # show what the agent changed
sandboxer return feat            # copy the changed files back to the project
sandboxer list                   # status of all sandboxes
sandboxer rm     feat            # delete the sandbox
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

Structured fields (dependency vendoring `srcs`, `extraMounts`, `env`) live in an
**optional** `sandboxer.yaml` (auto-discovered in the cwd, or `--config <file>`).
See `examples/sandboxer.yaml` and `examples/with-deps.yaml`.

```yaml
name: feature-x
mainSrc: .
backend: native
agent: claude
network:
  allowedDomains: [api.anthropic.com, registry.npmjs.org, github.com]
srcs:
  - { from: /abs/shared-lib, to: vendor/shared-lib, mode: rw }   # rw: returned to its source path on push
  - { root: /abs/schemas, glob: "**/*.proto", to: proto, mode: ro }
```

`srcs` are pulled into the sandbox (`sandboxer pull`); rw entries are copied back
to their source paths (`sandboxer push`), with protection against clobbering
local edits (unless `--force`).

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

Current coverage: **91.8%** total. Per package:

| Package             | Coverage |
| ------------------- | -------- |
| `cmd/sandboxer`     | 100.0%   |
| `internal/config`   | 100.0%   |
| `internal/backend`  | 94.9%    |
| `internal/runner`   | 94.7%    |
| `internal/egress`   | 94.3%    |
| `internal/registry` | 94.1%    |
| `internal/cli`      | 91.7%    |
| `internal/proxy`    | 90.3%    |
| `internal/srcs`     | 90.3%    |
| `internal/sandbox`  | 87.0%    |

Backend tests use fake engine/agent stubs on `PATH` and an isolated git config,
so they run without containers or touching real credentials; tests that need
`git`/`rsync` skip gracefully when those tools are absent.

## License

MIT — see [LICENSE](./LICENSE).
