# sandboxer — agent guide

Config-driven, multi-agent, containerized dev sandboxes (a Go CLI). This file is the agent-facing entry point.
The generalizable engineering conventions live in vendored **praxis skills** (below) — stated once there and
never restated here (`proc-skill-single-source`). This file holds only the delta: which skills are active, this
repo's parameters/deviations, and knowledge no skill owns.

## Skills (authoritative — vendored from praxis)

Materialized under `.claude/skills/` (gitignored). The committed source of truth is `praxis.toml` (desired set)
+ `.praxis/lock.json` (pinned hashes); run `praxis sync` after clone to regenerate the copies.

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
- **Config = `gopkg.in/yaml.v3`** (kpass uses TOML). Scalars resolve flag → `SANDBOXER_*` env → default;
  structured fields (roots/deps/extraMounts/env) come from an optional `.sandboxer.yaml` profile — a dotfile,
  centralized as `config.ConfigFileName` (one profile or several under a `profiles:` map). See `internal/config`.
- **User-facing error = `silentErr{err}`** in `internal/cli/cli.go` (kpass uses `UserError{Msg}`): it marks
  "the command already printed its own diagnostic", so `Run` returns exit 1 without re-printing. Same contract
  as `lang-go-error-handling`, different shape — there is no colored top-level reprint and no `130`/Canceled
  branch (the CLI is synchronous).
- **Lint:** `.golangci.yml` already matches `build-go-tooling`'s linter set verbatim. CI additionally enforces
  a **90% coverage floor** (the project-set value `lang-go-testing` / `ci-github-binary-release` refer to).
- **Release:** Linux **amd64 + arm64 only** today (no darwin matrix yet); `version` is hardcoded in
  `nix/package.nix` and bumped by `scripts/release.sh` — otherwise as `ci-github-binary-release`.

## Repo-only knowledge (owned by no skill)

- **Isolation backends** (`internal/backend`): `container` (podman/docker via the toolbox image) and `native`
  (Claude Code's own `/sandbox`, claude-only). `PersistentPreRunE` in `cli.go` blocks the mutating commands
  (create/enter/exec/run/rm/rm-all/use/stop) when running **inside** the container — only pull/push/show/list/diff
  are allowed there.
- **Sandbox state** (`internal/sandbox`): a sandbox is `.sandboxer/<slug>/` holding only the listed deps
  (each located by path suffix under your roots and copied in — nothing by default, no git involved), alongside
  `.sandboxer/_meta`, `_logs`, and a per-sandbox private `$HOME` at `.sandboxer/_home/<slug>` (v0.16.0). A
  generated `.sandboxer/.gitignore` keeps it all out of the user's repo.
- **Egress** (`internal/proxy`, `internal/egress`): outbound traffic is restricted to an allowlist
  (`network.allowedDomains` / `--allow-domains`) through a forward-proxy sidecar; disable with
  `SANDBOXER_NO_EGRESS=1`.
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
