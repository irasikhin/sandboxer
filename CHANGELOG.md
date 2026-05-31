# Changelog

All notable changes to this project are documented here. Format based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versioning follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

`scripts/release.sh` appends new sections automatically from
[Conventional Commits](https://www.conventionalcommits.org/) — please write
commit messages with that in mind. See [CONTRIBUTING.md](./CONTRIBUTING.md).

## [Unreleased]

### Changed

- Rewrote the entire CLI from Bash + Node to Go.

## [0.2.0] — 2026-05-31

### Added

- Config-driven multi-agent sandboxes (Bash implementation): native + podman/docker backends, egress allowlist, Nix profiles.

[Unreleased]: https://github.com/irasikhin/sandboxer/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/irasikhin/sandboxer/compare/v0.0.0...v0.2.0
