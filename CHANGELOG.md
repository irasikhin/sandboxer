# Changelog

All notable changes to this project are documented here. Format based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versioning follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

`scripts/release.sh` appends new sections automatically from
[Conventional Commits](https://www.conventionalcommits.org/) — please write
commit messages with that in mind. See [CONTRIBUTING.md](./CONTRIBUTING.md).

## [Unreleased]

## [0.20.1] — 2026-06-10

### Fixed

- re-sync profile snapshot on mutating ops (0f70c66)
- reload a flat .sandboxer.yaml on positional re-enter (e2ebcea)


## [0.20.0] — 2026-06-10

### Added

- per-profile tool packs baked into a toolbox image variant (38d2f94)
- MCP-server registry — seed agent config and allow domains (18a3575)


## [0.19.0] — 2026-06-10

### Added

- sandbox-aware shell prompt and orientation banner (df6375a)
- bake baseline tooling pack and delta git pager (60241ef)
- one-time per-sandbox `setup:` profile hook (2908a19)

### Docs

- plan terminal UX, agent tooling and plugins (1d23f07)
- document the setup hook and shell drop-ins (4142382)


## [0.18.3] — 2026-06-09

### Fixed

- scaffold the full default allowlist, not a 3-domain subset (414cb6d)


## [0.18.2] — 2026-06-09

### Tests

- raise engine-independent coverage above the 90% gate (14bab0c)

### CI

- stop uploading the coverage profile artifact (a0d6708)
- build and publish in one job (e7bab3f)
- run the nix job on the dedicated nix runner lane (5866be8)
- clone manually instead of actions/checkout (47fcb04)


## [0.18.1] — 2026-06-09

### Docs

- include Maven Central in example allowlists (f128d64)


## [0.18.0] — 2026-06-09

### Added

- chain the allowlist sidecar through an upstream proxy (51a0cf7)


## [0.17.0] — 2026-06-09

> **BREAKING:** the native backend is removed — sandboxer is container-only now.
> `backend: native`, `--backend native` and `SANDBOXER_BACKEND=native` are
> rejected; use `podman` or `docker`. The `--nice` flag and `SANDBOXER_NICE` env
> var are removed too (host niceness was native-only).

### Removed

- the native backend — sandboxer now runs every agent in a podman/docker container (fce071c)

### Docs

- adopt vendored Go skills + CLAUDE.md (df4ce6a)


## [0.16.0] — 2026-06-09

> **BREAKING:** the auto-discovered project config is now `.sandboxer.yaml`
> (was `sandboxer.yaml`) — rename your file. Container/headless runs no longer
> inherit host credentials: each sandbox has its own isolated `$HOME`, so run
> `claude login` inside the sandbox (or set `ANTHROPIC_API_KEY`) the first time.

### Added

- isolate agent home per sandbox; rename config to .sandboxer.yaml (c2a581b)


## [0.15.1] — 2026-06-06

### Tests

- add real end-to-end integration suite and coverage audit (2eae11d)


## [0.15.0] — 2026-06-05

### Added

- build the toolbox image with only docker/podman (no host nix) (198006a)


## [0.14.0] — 2026-06-05

### Added

- validate domain format in allowlist to catch typos early (b0ae686)


## [0.13.1] — 2026-06-05

### Docs

- translate tasks example to English for consistency (15109c5)


## [0.13.0] — 2026-06-05

### Added

- add --wide/-w to 'list' for full sandbox names (76c80ed)


## [0.12.0] — 2026-06-05

### Added

- add shell completion (bash, zsh, fish) (92cb7e1)


## [0.11.0] — 2026-06-05

### Added

- add 'sandboxer doctor' to diagnose the environment (feddf65)


## [0.10.0] — 2026-06-05

### Added

- include existing sandbox names in 'no sandbox selected' error (3794735)


## [0.9.1] — 2026-06-05

### Fixed

- clearer exec error hint when -- separator is missing (c753863)


## [0.9.0] — 2026-06-05

### Added

- refuse create without a profile instead of silently creating empty sandbox (c299e95)


## [0.8.0] — 2026-06-05

### Added

- add --dry-run to push so users can preview overwrites (360db2d)


## [0.7.0] — 2026-06-05

### Added

- require --force for rm-all to prevent accidental deletion (3d102da)


## [0.6.1] — 2026-06-05

### Fixed

- honor explicit --backend choice when that engine is installed (3eee62c)


## [0.6.0] — 2026-06-04

### Added

- **`sandboxer init`** scaffolds a commented `sandboxer.yaml` (seeded with the
  effective defaults) so there's a concrete config to edit instead of relying on
  silent defaults; refuses to clobber an existing file without `--force`.
- **Auto-scaffold.** `create`/`enter` in a project with no config now write (and
  announce) a default `sandboxer.yaml` and use it for the run, instead of
  falling back to silent defaults. An explicit `-f`, an existing config, or
  `SANDBOXER_NO_SCAFFOLD=1` skip it.

### Changed

- **Runtime transparency.** `create`/`enter`/`exec` (and `show`) now print the
  resolved settings they use — `agent`, `backend`, `model`, egress status,
  profile source, dep count — so it's never a mystery what config applied. First
  creation of the `.sandboxer/` state tree is announced, the misleading
  post-run "push" hint is replaced with the actual copy-back result, and the
  remaining user-facing `srcs` wording is now `deps`.
- Root `--help` surfaces the active-sandbox (`use`), multi-profile and egress
  features; `pull`/`push` gained examples showing the idempotent-vs-overwrite
  asymmetry.

## [0.5.0] — 2026-06-04

### Added

- **Multi-profile files.** A `sandboxer.yaml` may now hold several named
  profiles under a `profiles:` map instead of one profile per file. A shared
  `defaults:` block is auto-applied under every profile (own fields win; `env`
  merges key-by-key); inheritance between profiles uses plain YAML anchors
  (`&api` / `<<: *api`), no special key. `default:` names the profile used when
  none is given. `sandboxer create <name>` selects a section by name; a batch
  run uses the default/sole profile. The flat one-profile-per-file form is
  unchanged. See `examples/multi-profile.yaml`.

## [0.4.0] — 2026-06-04

### Added

- **Named profiles.** `-f`/`--config` now accepts a file, a *directory* of
  profiles, or the *name* of a profile in a global store
  (`~/.config/sandboxer/profiles/`; override with `$SANDBOXER_PROFILES`, follows
  `$XDG_CONFIG_HOME`). A profile's name is its file's base name unless an
  explicit `name:` overrides it. A bare positional that matches a stored profile
  is used as that profile; otherwise it stays a plain slug, so existing
  `create <slug>` usage is unchanged. New `sandboxer profiles` lists the store
  (or a `-f <dir>`). See `examples/profiles/`.

### Changed

- Sandbox content is now **deps-only**: each `dep` is located by path suffix
  under the configured `roots` and copied into `.sandboxer/<slug>/`
  (depsync-style), replacing the wholesale rsync copy of the project. Use
  `sandboxer pull` / `push` to move deps in and back.
- `sandboxer run` auto-discovers `sandboxer.yaml` in the cwd (like the lifecycle
  commands) when `--config` is omitted, so both entry points resolve a profile
  identically.
- `--mem` / `--cpu` / `--wall` now also apply to the container backend
  (`--memory` / `--cpus` / an in-container `timeout`), not only the native
  backend — the `run` banner's limits are now enforced on both.

### Removed

- The old `srcs` / `mainSrc` config — use `roots` + `deps`.

### Security

- The container egress allowlist now **fails closed**: if the allowlist is
  required but the proxy can't start (or no domains are allowed), the run is
  refused instead of silently falling back to an open bridge network.
- `SANDBOXER_NO_EGRESS=1` is now honoured by `enter`/`exec` too (previously only
  the batch `run`), matching the documented behaviour.
- `deps` that are absolute or contain `../` are refused instead of letting a
  pull write outside the sandbox.
- Container credential setup now **fails closed**: a failed ephemeral copy of an
  agent's config aborts the run instead of proceeding unauthenticated (or
  mounting a missing path).
- `SECURITY.md` now documents two surprising-but-intentional behaviours: a
  configured upstream proxy *replaces* the egress allowlist, and the `native`
  backend inherits the full host environment (secrets included).

### Fixed

- Root `--help` no longer describes the removed git / rsync / `mainSrc` model.
- The native backend now errors for an agent without an OS sandbox (anything but
  `claude`) instead of silently running it un-sandboxed on the host.
- `run`, `exec` and `create` now ship `--help` examples; `list`/`diff` output is
  tidier (ellipsis on truncation, no empty diff sections).
- The automatic copy-back after `enter`/`exec` now **exits non-zero when the
  push fails** (previously it only printed the error), so a failed return of
  work can't masquerade as success; `enter` also propagates the container's exit
  code (like `exec`).
- A corrupt dependency manifest now fails `push`/`diff` instead of silently
  restoring nothing; a `dep` with multiple matches lists the alternatives.
- `sandboxer run` now **exits non-zero when any agent fails** (or a sandbox
  can't be created) and prints an `N ok, M failed` tally, instead of reporting
  success for a partial batch. Worker log-creation and container setup failures
  (egress, credentials) are surfaced and counted rather than silently dropped.
  It still reports the number of sandboxes that launched and rejects a malformed
  `--mem`/`--cpu`/`--wall` up front instead of failing asynchronously.

## [0.3.0] — 2026-06-01

### Changed

- Rewrote the entire CLI from Bash + Node to Go.
- Replaced the git-based snapshot/merge with a git-free model: a sandbox is a
  plain rsync copy of the project, and `sandboxer return` copies the changed
  files back to the source. `.git` is excluded from the copy, so a sandbox
  never carries the project's git remotes.

### Added

- Test suite across all packages (91.8% statement coverage).
- MIT LICENSE and README badges (CI, coverage, Go, license).

### Removed

- The `gitx` package, in-copy snapshot branches, and the git-based
  merge / cherry-pick / `--patch` return path.

## [0.2.0] — 2026-05-31

### Added

- Config-driven multi-agent sandboxes (Bash implementation): native + podman/docker backends, egress allowlist, Nix profiles.

[Unreleased]: https://github.com/irasikhin/sandboxer/compare/v0.3.0...HEAD
[0.6.0]: https://github.com/irasikhin/sandboxer/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/irasikhin/sandboxer/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/irasikhin/sandboxer/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/irasikhin/sandboxer/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/irasikhin/sandboxer/compare/v0.0.0...v0.2.0
[0.6.1]: https://github.com/irasikhin/sandboxer/compare/v0.6.0...v0.6.1
[0.7.0]: https://github.com/irasikhin/sandboxer/compare/v0.6.1...v0.7.0
[0.8.0]: https://github.com/irasikhin/sandboxer/compare/v0.7.0...v0.8.0
[0.9.0]: https://github.com/irasikhin/sandboxer/compare/v0.8.0...v0.9.0
[0.9.1]: https://github.com/irasikhin/sandboxer/compare/v0.9.0...v0.9.1
[0.10.0]: https://github.com/irasikhin/sandboxer/compare/v0.9.1...v0.10.0
[0.11.0]: https://github.com/irasikhin/sandboxer/compare/v0.10.0...v0.11.0
[0.12.0]: https://github.com/irasikhin/sandboxer/compare/v0.11.0...v0.12.0
[0.13.0]: https://github.com/irasikhin/sandboxer/compare/v0.12.0...v0.13.0
[0.13.1]: https://github.com/irasikhin/sandboxer/compare/v0.13.0...v0.13.1
[0.14.0]: https://github.com/irasikhin/sandboxer/compare/v0.13.1...v0.14.0
[0.15.0]: https://github.com/irasikhin/sandboxer/compare/v0.14.0...v0.15.0
[0.15.1]: https://github.com/irasikhin/sandboxer/compare/v0.15.0...v0.15.1
[0.16.0]: https://github.com/irasikhin/sandboxer/compare/v0.15.1...v0.16.0
[0.17.0]: https://github.com/irasikhin/sandboxer/compare/v0.16.0...v0.17.0
[0.18.0]: https://github.com/irasikhin/sandboxer/compare/v0.17.0...v0.18.0
[0.18.1]: https://github.com/irasikhin/sandboxer/compare/v0.18.0...v0.18.1
[0.18.2]: https://github.com/irasikhin/sandboxer/compare/v0.18.1...v0.18.2
[0.18.3]: https://github.com/irasikhin/sandboxer/compare/v0.18.2...v0.18.3
[0.19.0]: https://github.com/irasikhin/sandboxer/compare/v0.18.3...v0.19.0
[0.20.0]: https://github.com/irasikhin/sandboxer/compare/v0.19.0...v0.20.0
[0.20.1]: https://github.com/irasikhin/sandboxer/compare/v0.20.0...v0.20.1
