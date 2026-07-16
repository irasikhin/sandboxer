# Changelog

All notable changes to this project are documented here.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.45.0] — 2026-07-16

### Added

- default worktrees to ./sandboxes in the project, auto-gitignored (ce30111)


## [0.44.0] — 2026-07-16

### Added

- group worktrees by repo; relocatable root via worktreesDir (f1e57f9)


## [0.43.0] — 2026-07-16

### Added

- clean --detached — sweep set-aside dropped sources alone (7f075d3)


## [0.42.0] — 2026-07-16

### Added

- explicit branch per source; worktrees live beside the project (a12d4d8)

### Fixed

- sticky recorded branch — a default rename must not reset sandboxes (b98ca74)

### Docs

- record the sticky-branch invariant (3632763)


## [0.41.0] — 2026-07-16

### Added

- flatten the image schema — packages/files/env + a plain overlay file (9c3d843)


## [0.40.0] — 2026-07-16

### Added

- print sandbox worktree paths with `sandboxer path` (ccdded0)


## [0.39.0] — 2026-07-16

### Added

- java 25 in the base image; remove the agents credential passthrough (7d42b96)


## [0.38.0] — 2026-07-16

### Added

- everyday toolchain + rootless nested podman in the base image (460d250)


## [0.37.0] — 2026-07-16

### Added

- list the connected sources on every enter (67b7b99)
- drop the -sb suffix — reuse existing feat/<slug> branches (1bb0e22)


## [0.36.0] — 2026-07-16

### Added

- show the binary version on every create/enter/exec banner (4d6b055)


## [0.35.1] — 2026-07-16

### Fixed

- announce each slow step of session convergence (a2eb14b)


## [0.35.0] — 2026-07-15

### Added

- stop managing tmux — multiplexing is the user's own (196c311)

### Fixed

- default the container to a UTF-8 locale (85e78a4)


## [0.34.0] — 2026-07-15

### Added

- rename the network block to egress with an enabled toggle (57db7ed)

### Fixed

- reach platform.claude.com by default so Claude Code connects (cd626d8)


## [0.33.1] — 2026-07-15

### Fixed

- never let a substituter wedge the image build (a0c719c)


## [0.33.0] — 2026-07-15

### Added

- nix config — sandboxer.nix replaces YAML, host nix required (eafccf8)


## [0.32.0] — 2026-07-15

### Added

- srcs is always explicit — no implicit current-dir source (c11af59)
- profiles live in one config file — drop the store and -f dirs (d2db9c9)

### Fixed

- name the real cause when srcs is empty; warn on empty include (7ccb3c4)


## [0.31.0] — 2026-07-15

### Added

- name sandbox branches feat/<slug>-sb instead of sandbox/<slug> (6bc3f71)
- relocate config to sandboxer.yaml + sandboxer-image.nix at repo root (556dfca)
- srcs model — multi-repo sources, git never enters the container (102bc40)


## [0.30.0] — 2026-07-14

### Added

- accept --backend on create (4762980)
- config get/set/unset; move profile init/edit/validate to config (b40ec55)
- remove config inheritance — no defaults:, no global config (6a74cf0)

### Fixed

- retire copy-in-era help text and ghost commands (b0bb7c7)
- align enter's Use string, add enter/rm help, de-collide the profile-list default marker (f29de25)

### Refactored

- add comment-preserving yaml editor, key registry, LoadDocumentBytes (5394372)

### Docs

- fix stale examples, dedupe restated config docs, drop the PoC eval matrix (f821cde)


## [0.29.0] — 2026-07-13

### Refactored

- remove mcp: — the worktree redesign made it redundant (02d0cce)

### Docs

- drop stale copy-mode references left after the git-only migration (29a3a82)

### Tests

- drop obsolete copy-mode lifecycle integration tests (09cb6ab)


## [0.28.0] — 2026-07-13

### Added

- wire resource limits (memory/cpus/pids) (ccd9555)
- domain-routed upstream proxies + fold sidecar config into session freshness (df1319d)
- experimental macOS support — SANDBOXER_CONTAINER_USER escape hatch + setup docs (db2c33d)

### Fixed

- build the itest smoke image via nix, not `docker pull` from Docker Hub (69ea3f5)
- mount repo config/hooks read-only to close git-dir RCE (3a13c4a)
- update integration tests for removed Wall + new egress.Up signature (1e03950)

### Refactored

- remove the model: knob (8b05617)
- move proxy/noProxy under network: (0b521f7)
- remove agent: and agentProxy: (0d84165)
- git-only sandboxes — drop the copy-mode fallback (4e9d536)

### Docs

- drop references to the removed run command (14eaeaa)
- rewrite SECURITY.md + docs/architecture.md to match reality (3e85ebd)

### Chores

- prune dead resource/parallelism knobs (a7d19dd)
- drop dead launch-command + credential-dir surface (7264db3)


## [0.27.0] — 2026-07-09

### Added

- back sandboxes with git worktrees instead of copy-in (4a20eff)

### Fixed

- discover .sandboxer/config.yaml under --src, not just the cwd (a7d5cef)


## [0.26.3] — 2026-07-08

### Fixed

- make the Jenkins e2e build work on the homelab agent (94b7c65)
- make the Jenkins e2e resilient to throttled egress (01bb1fe)
- route Jenkins egress through the AmneziaWG proxy (91cfa3f)
- build both images + share tmp with dind so bind-mount tests pass (1b7d13f)
- retry the alpine pull (transient 503 through the egress proxy) (5d2b662)

### Docs

- correct the test-coverage audit and note the Jenkins e2e run (0296f5c)

### Tests

- repair rotted e2e suite for the squid egress path (688f7ed)
- add real-behaviour e2e for the squid proxy mechanism (0ef76e8)
- add real-engine e2e for agent env/home isolation and recreate (09a891a)
- skip live-internet egress tests where containers have no direct egress (afdc8c7)

### CI

- add container e2e pipeline for the homelab Jenkins (c0b6534)


## [0.26.2] — 2026-06-30

### Fixed

- correct stale proxy syntax in profile scaffold (2eff5fa)


## [0.26.1] — 2026-06-23

### Fixed

- wire llm-agents binary cache into the embedded flake (f8847a3)


## [0.26.0] — 2026-06-23

### Added

- single proxy URL + per-agent and global proxy (1a0997e)

### Fixed

- give the proxy sidecar the host-gateway alias (185ba71)


## [0.25.0] — 2026-06-19

### Added

- list profiles across project, global and store (aff1cfc)

### CI

- bump actions/checkout from 4 to 7 (1307929)


## [0.24.1] — 2026-06-18

### CI

- build proxy image every run, both images on tag release (a31fdc9)


## [0.24.0] — 2026-06-18

### Added

- host-only sandboxer — squid egress, XDG config/data split, CLI regroup (24ff79e)


## [0.23.0] — 2026-06-11

### Added

- A **direnv hook**: `sandboxer hook direnv` prints the active sandbox as
  host-shell exports (`SANDBOXER_SLUG`/`SANDBOXER_SRC`, plus the recorded
  `SANDBOXER_MODEL`/`SANDBOXER_BACKEND`/`SANDBOXER_ALLOW_DOMAINS`) for an
  `.envrc`, with a bundled `use_sandboxer` helper
  (`contrib/direnv/use_sandboxer.sh`) so an `.envrc` can `use sandboxer` and
  reload on `sandboxer use <slug>`. It is read-only — nothing is built or
  started on `cd` — and a no-op (exit 0) outside a sandboxer project or with no
  active sandbox, so it is safe to call unconditionally.
- An optional **global config** that merges *under* the project config. It is a
  full document (`defaults:` plus an optional `profiles:` map) read from
  `$SANDBOXER_CONFIG` → `$XDG_CONFIG_HOME/sandboxer/config.yaml` →
  `~/.config/sandboxer/config.yaml`; an absent file is a clean no-op. Its
  `defaults:` sit below the project's so the project always wins — effective
  precedence is flags > project profile section > project defaults > GLOBAL
  defaults > `SANDBOXER_*` env > built-in. The per-field merge reuses the
  existing one (`env` key-wise, `image:` per field), so the global can pin the
  image revisions while a project adds its own packages. A profile name resolves
  project → global → the named-profile store. `roots:`/`deps:` are
  project-specific and discouraged at global scope.
- **`sandboxer recreate [--full]`** — rebuild a sandbox from its profile in
  one step: the working copy, manifest and setup stamp are wiped (setup re-runs
  on the next enter) and deps are pulled fresh. The private agent home —
  logins, shell history — is preserved; `--full` wipes it too, making recreate
  equivalent to `rm` + `create`. (6050cf9)
- **Safe push**: `pull` records a signature of each rw origin in the manifest,
  and `push` — including the automatic copy-back after `enter`/`exec` — skips
  any origin edited on the host since that pull instead of silently
  overwriting it; `push --force` restores the wholesale overwrite. Also fixes
  the in-container `pull --force` self-destruct: an origin that IS the sandbox
  copy is kept, never copied onto itself. (af4d4ce)
- The current directory is always searched as an **implicit last root**, so a
  project-local dep needs no `roots:` stanza; explicit roots win the
  deterministic first-match. (1638993)
- **Agent context files**: `CLAUDE.md`, `AGENTS.md` and `.claude/` (those that
  exist in the project root) are copied read-only to the sandbox root, so
  agents see the project's instructions; a non-empty `context:` profile list
  replaces the default set. Refreshed on `pull`, never pushed back. (9490dc3)
- A **JSON Schema** for the profile (`schema/config.schema.json`), generated
  from the same Go structs the strict parser uses; the scaffolded config opens
  with a `yaml-language-server` header so editors flag typos before sandboxer
  runs. (1f16586)
- `doctor` (and `create`) warn when the repo's gitignore hides
  `.sandboxer/config.yaml` — a root-level `.sandboxer/` rule defeats the
  generated allowlist. (5d53d5c)

### Changed (BREAKING)

- The project profile and the nix image hook now live under the sandboxer-owned
  state dir: `./.sandboxer.yaml` → `.sandboxer/config.yaml` and
  `./sandbox-image.nix` → `.sandboxer/image.nix`. The old root-level locations are
  no longer read. The generated `.sandboxer/.gitignore` becomes an allowlist that
  commits only `config.yaml`, `image.nix` and itself while keeping
  `_meta`/`_home`/`_logs`/`<slug>` ignored; an existing blanket `*` gitignore is
  upgraded in place. `sandboxer init`/auto-scaffold write into `.sandboxer/`, and
  `doctor` (and discovery) flag a lingering `./.sandboxer.yaml`.

  Migrate with:

  ```sh
  mkdir -p .sandboxer && git mv .sandboxer.yaml .sandboxer/config.yaml && git mv sandbox-image.nix .sandboxer/image.nix
  ```
- Deps are vendored under **`<sandbox>/workspace/`** instead of the sandbox
  root, keeping the root free for the agent context files. Existing sandboxes
  keep the old flat layout (a pull prints a NOTE) — run
  `sandboxer recreate <slug>`; `setup:` scripts that `cd` into a dep need the
  `workspace/` prefix. (fa602ec)
- `push` no longer overwrites an origin edited on the host since the last pull
  (use `push --force`). Manifests written by older versions carry no
  signatures, so the first push after upgrading reports skips — re-pull or
  `push --force` once. (af4d4ce)

## [0.22.1]

Initial public release.

sandboxer runs several autonomous coding agents in parallel, each in its own
isolated, containerized dev sandbox, on a local Linux machine:

- Container isolation via docker or podman, using a nix-built toolbox image.
- Profile-based configuration (`.sandboxer.yaml`): roots + deps, env, extra mounts.
- Dependency vendoring — only the listed deps are copied into each sandbox.
- Egress allowlist enforced by a forward-proxy sidecar (fail-closed).
- Persistent tmux sessions (detach / reattach) and per-profile custom images.
- A batch runner that drives one agent per task across parallel sandboxes.
[0.23.0]: https://github.com/irasikhin/sandboxer/compare/v0.22.1...v0.23.0
[0.24.0]: https://github.com/irasikhin/sandboxer/compare/v0.23.0...v0.24.0
[0.24.1]: https://github.com/irasikhin/sandboxer/compare/v0.24.0...v0.24.1
[0.25.0]: https://github.com/irasikhin/sandboxer/compare/v0.24.1...v0.25.0
[0.26.0]: https://github.com/irasikhin/sandboxer/compare/v0.25.0...v0.26.0
[0.26.1]: https://github.com/irasikhin/sandboxer/compare/v0.26.0...v0.26.1
[0.26.2]: https://github.com/irasikhin/sandboxer/compare/v0.26.1...v0.26.2
[0.26.3]: https://github.com/irasikhin/sandboxer/compare/v0.26.2...v0.26.3
[0.27.0]: https://github.com/irasikhin/sandboxer/compare/v0.26.3...v0.27.0
[0.28.0]: https://github.com/irasikhin/sandboxer/compare/v0.27.0...v0.28.0
[0.29.0]: https://github.com/irasikhin/sandboxer/compare/v0.28.0...v0.29.0
[0.30.0]: https://github.com/irasikhin/sandboxer/compare/v0.29.0...v0.30.0
[0.31.0]: https://github.com/irasikhin/sandboxer/compare/v0.30.0...v0.31.0
[0.32.0]: https://github.com/irasikhin/sandboxer/compare/v0.31.0...v0.32.0
[0.33.0]: https://github.com/irasikhin/sandboxer/compare/v0.32.0...v0.33.0
[0.33.1]: https://github.com/irasikhin/sandboxer/compare/v0.33.0...v0.33.1
[0.34.0]: https://github.com/irasikhin/sandboxer/compare/v0.33.1...v0.34.0
[0.35.0]: https://github.com/irasikhin/sandboxer/compare/v0.34.0...v0.35.0
[0.35.1]: https://github.com/irasikhin/sandboxer/compare/v0.35.0...v0.35.1
[0.36.0]: https://github.com/irasikhin/sandboxer/compare/v0.35.1...v0.36.0
[0.37.0]: https://github.com/irasikhin/sandboxer/compare/v0.36.0...v0.37.0
[0.38.0]: https://github.com/irasikhin/sandboxer/compare/v0.37.0...v0.38.0
[0.39.0]: https://github.com/irasikhin/sandboxer/compare/v0.38.0...v0.39.0
[0.40.0]: https://github.com/irasikhin/sandboxer/compare/v0.39.0...v0.40.0
[0.41.0]: https://github.com/irasikhin/sandboxer/compare/v0.40.0...v0.41.0
[0.42.0]: https://github.com/irasikhin/sandboxer/compare/v0.41.0...v0.42.0
[0.43.0]: https://github.com/irasikhin/sandboxer/compare/v0.42.0...v0.43.0
[0.44.0]: https://github.com/irasikhin/sandboxer/compare/v0.43.0...v0.44.0
[0.45.0]: https://github.com/irasikhin/sandboxer/compare/v0.44.0...v0.45.0
