# Changelog

All notable changes to this project are documented here.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
