# Changelog

All notable changes to this project are documented here. Format based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versioning follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

`scripts/release.sh` appends new sections automatically from
[Conventional Commits](https://www.conventionalcommits.org/) — please write
commit messages with that in mind. See [CONTRIBUTING.md](./CONTRIBUTING.md).

## [Unreleased]

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

### Fixed

- Root `--help` no longer describes the removed git / rsync / `mainSrc` model.
- The native backend now errors for an agent without an OS sandbox (anything but
  `claude`) instead of silently running it un-sandboxed on the host.
- `run`, `exec` and `create` now ship `--help` examples; `list`/`diff` output is
  tidier (ellipsis on truncation, no empty diff sections).

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
[0.3.0]: https://github.com/irasikhin/sandboxer/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/irasikhin/sandboxer/compare/v0.0.0...v0.2.0
