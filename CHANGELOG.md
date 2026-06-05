# Changelog

All notable changes to this project are documented here. Format based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versioning follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

`scripts/release.sh` appends new sections automatically from
[Conventional Commits](https://www.conventionalcommits.org/) — please write
commit messages with that in mind. See [CONTRIBUTING.md](./CONTRIBUTING.md).

## [Unreleased]

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
