# Changelog

All notable changes to this project are documented here.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

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
