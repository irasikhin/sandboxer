# sandboxer — agent guide

Config-driven, multi-agent, containerized dev sandboxes (a Go CLI). This file is the agent-facing entry point.
The generalizable engineering conventions live in vendored **praxis skills** (below) — stated once there and
never restated here (`proc-skill-single-source`). This file holds only the delta: which skills are active, this
repo's parameters/deviations, and knowledge no skill owns.

## Skills (authoritative — vendored from praxis)

The committed manifest is `praxis.toml` (the desired set). The lock and the materialized copies
(`.praxis/`, `.claude/skills/`) are gitignored and regenerated locally by `praxis sync` (praxis is
optional author-tooling — not required to build or contribute; the same conventions are also enforced
by `.golangci.yml` and CI).

<!-- praxis:skills BEGIN -->
arch-go-cli, lang-go-style, lang-go-error-handling, lang-go-testing, build-go-tooling, ci-github-binary-release,
proc-conventional-commits, proc-branch-pr-workflow, proc-repo-hygiene, proc-doc-as-code, proc-security-posture,
proc-skill-single-source, flow-incremental-pr, flow-root-cause
<!-- praxis:skills END -->

These own the generalizable rules — Go architecture, style, errors, testing, build/lint, binary release,
commits, PR flow, security posture. **Do NOT restate them here or in CONTRIBUTING.md — link instead.**

## This repo: parameters & deviations

Fills for the skills' placeholders, and the local choices that differ from the kpass reference `arch-go-cli`
was extracted from:

- **Module / app:** `github.com/irasikhin/sandboxer`; binary `sandboxer`; Go **1.24**.
- **CLI parser = `spf13/cobra`** — `arch-go-cli` is parser-agnostic; the kpass reference uses `alecthomas/kong`.
  Commands are `*cobra.Command` factories that self-register via `register()` in each command file's `init()`;
  the root is assembled from `commandFactories` in `internal/cli/cli.go` (no central command list). The
  `Run(args, stdin, stdout, stderr) int` stdio seam is exactly as `arch-go-cli` describes.
- **Config = NIX** (kpass uses TOML): `sandboxer.nix` at the project root, evaluated to JSON via host
  `nix-instantiate --eval --strict --json` under restrict-eval (`config.EvalConfig`; nix on the host is a
  HARD requirement of the CLI — image builds still run in a container). Scalars resolve flag →
  `SANDBOXER_*` env → default; structured fields (srcs/extraMounts/env/image) come from the config — one
  profile or several under a `profiles` attrset, reuse via ordinary nix (let//). The image hook lives
  INLINE as `image.hook` (string) or via `image.nix` path. Strict decode: JSON `DisallowUnknownFields` +
  removedKeys hints. Only `config init|edit|validate` exist — no get/set/unset (no comment-preserving nix
  editing from Go). See `internal/config`.
- **User-facing error = `silentErr{err}`** in `internal/cli/cli.go` (kpass uses `UserError{Msg}`): it marks
  "the command already printed its own diagnostic", so `Run` returns exit 1 without re-printing. Same contract
  as `lang-go-error-handling`, different shape — there is no colored top-level reprint and no `130`/Canceled
  branch (the CLI is synchronous).
- **Lint:** `.golangci.yml` already matches `build-go-tooling`'s linter set verbatim. CI additionally enforces
  a **90% coverage floor** (the project-set value `lang-go-testing` / `ci-github-binary-release` refer to).
- **Release:** Linux **amd64 + arm64 only** today (no darwin matrix yet); `version` is hardcoded in
  `nix/package.nix` and bumped by `scripts/release.sh` — otherwise as `ci-github-binary-release`.

## Repo-only knowledge (owned by no skill)

- **Isolation backend** (`internal/backend`): `container` (podman/docker via the toolbox image). sandboxer is a
  HOST tool — its binary is NOT baked into the toolbox image, so it is normally absent inside the sandbox;
  `PersistentPreRunE` in `cli.go` is a belt-and-suspenders **deny-all** (every command refuses when
  `SANDBOXER_IN_CONTAINER` is set, injected per-run by `commonArgs`).
- **Config vs data split** (`internal/config`, `internal/sandbox`): the committed config is ONE file at
  the repo root — `sandboxer.nix` (image hook inline).
  ALL runtime state lives OUTSIDE the repo under `config.StateDir(project)` =
  `$XDG_STATE_HOME/sandboxer/<project-id>` (`<project-id>` = basename + short hash of the abs path) — the
  `_meta`/`_logs`/`_home/<slug>`/`<slug>` dirs, so credentials/scratch can never be committed. `sandboxer clean`
  wipes that state (config stays).
- **Sandbox backing = srcs** (`internal/worktree`, `internal/sandbox/srcs.go`): a sandbox exposes SOURCES —
  `srcs: [{src, include, branch}]` — each a git worktree at `<stateDir>/<slug>/<repo>/` on branch
  `feat/<slug>-sb` (or the entry's `branch:`, which ADOPTS an existing worktree of that branch, incl. the main
  checkout), narrowed by gitignore-style `include` patterns via **non-cone** `sparse-checkout`. srcs is ALWAYS
  explicit — an empty list is rejected; the scaffolded config seeds `srcs: [{src: .}]`. Relative src paths
  resolve against the PROJECT ROOT (not the profile file's dir). **Git never enters the container**: no git-dir mounts, no `GIT_CONFIG_*` — the
  container gets one stable rw mount of `<slug>/` (plus adopted paths), so the sparse worktree contents ARE the
  wall and srcs edits are picked up by every enter/exec (a LIVE session sees them immediately); commits happen
  on the host. Resolved sources are recorded at `_meta/<slug>.srcs.json`; dropped sources move to `_detached/`
  (data-safe). Teardown removes managed worktrees only and KEEPS branches (`recreate --full` deletes just the
  auto-named ones). **git-only:** a non-git source is rejected with an init hint; non-git trees come in via
  `extraMounts`.
- **Egress** (`internal/egress`): outbound traffic is restricted to an allowlist
  (`network.allowedDomains` / `--allow-domains`) through a **squid** sidecar (the `sandboxer-proxy` image, built
  beside the toolbox image; `config.ProxyImage()`) running a generated `squid.conf` — the binary is never in the
  network path. Disable with `SANDBOXER_NO_EGRESS=1`.
- **Agent registry** (`internal/registry/registry.json`): the single-source catalog of agents — embedded in the
  binary AND consumed by the Nix flake (`llm-agents.nix`). Edit the JSON, never duplicate it.
- **Toolbox image** (`internal/toolbox` + flake `dockerTools.buildLayeredImage`): the OCI image with the agents
  baked in; built via `nix run .#build-image`.
- **Integration tests** (`internal/itest`, `//go:build integration`): drive a real engine and skip cleanly when
  prerequisites are missing; run via `scripts/itest.sh`. Excluded from CI and the coverage gate. (The general
  test conventions are `lang-go-testing`; this is only the harness delta.)

## Working in this repo

- Branch off `main`; land via PR; stage explicit paths, never `git add -A` (`flow-incremental-pr`,
  `proc-branch-pr-workflow`).
- Green gate before commit: `gofmt` + `go vet` + `golangci-lint run` + `go test ./...` (`build-go-tooling`).
- Human contributor runbook (local setup, integration suite, release steps) lives in `CONTRIBUTING.md`.
