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

- **Module / app:** `github.com/irasikhin/sandboxer`; binary `sandboxer`; Go **1.25.8** (the floor is a security floor — go1.24 carries reachable stdlib CVEs GO-2026-4601/4602, and CI runs govulncheck on the toolchain go.mod selects).
- **CLI parser = `spf13/cobra`** — `arch-go-cli` is parser-agnostic; the kpass reference uses `alecthomas/kong`.
  Commands are `*cobra.Command` factories that self-register via `register()` in each command file's `init()`;
  the root is assembled from `commandFactories` in `internal/cli/cli.go` (no central command list). The
  `Run(args, stdin, stdout, stderr) int` stdio seam is exactly as `arch-go-cli` describes.
- **Config = NIX** (kpass uses TOML): `sandboxer.nix` at the project root, evaluated to JSON via host
  `nix-instantiate --eval --strict --json` under restrict-eval (`config.EvalConfig`; nix on the host is a
  HARD requirement of the CLI — image builds still run in a container). Scalars resolve flag →
  `SANDBOXER_*` env → default; structured fields (srcs/extraMounts/env/image) come from the config — one
  profile or several under a `profiles` attrset, reuse via ordinary nix (let//). Image customization is
  FLAT data — `image.{packages,files,env}` + `image.overlay` (a file with a PLAIN nixpkgs overlay
  `final: prev: {…}`, rendered to the build ctx as overlay.nix; files/env go as files.json/env.json).
  Strict decode: JSON `DisallowUnknownFields` +
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
  the repo root — `sandboxer.nix` (image customization included; overlay is a separate .nix file).
  The sandbox WORKTREES live IN the project at `sandbox.SandboxesRoot(project)` = `./sandboxes/`
  (auto-added to the project .gitignore; `_detached/` included; relocatable per profile via
  `worktreesDir` — abs/~/project-relative; in-project overrides are git-ignored too; clean never
  removes a root wholesale, only the sandbox dirs); the rest of the runtime state lives under
  `config.StateDir(project)` = `$XDG_STATE_HOME/sandboxer/<project-id>` (`<project-id>` = basename + short
  hash of the abs path) — the `_meta`/`_logs`/`_home/<slug>` dirs. Both are outside the repo, so
  credentials/scratch can never be committed. `sandboxer clean` wipes both (config stays).
- **Sandbox backing = srcs** (`internal/worktree`, `internal/sandbox/srcs.go`): a sandbox exposes SOURCES —
  `srcs: [{src, branch, include}]` — each a git worktree at `<sandboxesRoot>/<slug>/<branch>/<repo>/`
  (grouped by BRANCH, repo basename as the leaf — deduped by a short hash, and a layout that would nest one
  worktree inside another is refused; a managed worktree whose assigned path changed while (repo, branch) did
  not is MOVED in place — `git worktree move`, uncommitted work and ignored caches kept, falling back to
  detach when the target is occupied; root relocatable per profile via `worktreesDir`). `branch:` is
  REQUIRED — no default naming, a missing branch is an error (the error hints the recorded branch); a branch
  already checked out elsewhere (incl. the main checkout) is ADOPTED — except a set-aside `_detached/` checkout
  (re-attached to its managed path instead) or a stale registration whose dir was hand-deleted (pruned, checked
  out fresh — `rm -rf ./sandboxes` self-heals on the next enter; `_meta/<slug>.gen` flips the session hash so
  the pre-deletion session container is rebuilt, not reused with dead mounts). Trees are narrowed by
  `include` — a list of DIRECTORIES (literal anchored paths or ant-style directory patterns: `/services/*/`,
  `**/proto/` — a whole `**` segment matches any depth, expanded on disk per mount computation, zero matches =
  error) that narrows the container's MOUNT SET, never the worktree (the host tree is always a complete
  checkout, so an IDE can open it; negations/unanchored paths are rejected, and patterns match directories
  only, never files — a mount names a path, not a file set; see `docs/view-mounts-design.md`). srcs is ALWAYS explicit — an empty list is rejected;
  the scaffolded config seeds `srcs = [{src = "."; branch = "feat/<name>";}]`. Relative src paths
  resolve against the PROJECT ROOT (not the profile file's dir). **Git never enters the container**: no git-dir mounts, no `GIT_CONFIG_*` — the
  MOUNT SET is the wall (`sandbox.Mounts` decides it): unnarrowed = one stable rw mount of `<slug>/` (plus
  adopted paths), so srcs edits are picked up by every enter/exec (a LIVE session sees them immediately);
  narrowed = `<slug>/` is NOT mounted at all (the host worktrees under it are complete — that absence IS the
  boundary) and each include dir is mounted rw at its own path. Commits happen on the host. Resolved sources are recorded at `_meta/<slug>.srcs.json`; a dropped source's worktree is
  REMOVED when clean (branch kept) and moved to `_detached/` only when it holds uncommitted work (a worktree in
  detached-HEAD state is left in place, and a non-worktree dir with content is renamed aside, never
  deleted). Teardown removes managed worktrees only and KEEPS branches (`recreate --full`
  deletes just the ones sandboxer minted — recorded per source). **git-only:** a non-git source is rejected
  with an init hint; non-git trees come in via `extraMounts`.
  **Remote srcs** (`internal/worktree/remote.go`): a `src` that is a git URL
  (`IsRemoteURL`: https/ssh/git/file scheme or scp-like `git@host:path`) is CLONED once into
  `<stateDir>/_remotes/<name>-<hash>/` (detached HEAD so every branch is free for `worktree add`), then treated
  as a normal local repo — `RepoRoot` = the cache. Clone-once: `resolveSrcs` clones if absent; `recreate`
  re-fetches (`RefreshRemotes` → `worktree.Fetch`, ff-safe). `branch:` on a remote → `PrepareBranch` checks out
  `origin/<branch>`. The cache is kept across teardown (shared, like a local repo), wiped by `clean`.
- **Egress** (`internal/egress`): outbound traffic is restricted to an allowlist
  (`egress.allowedDomains` / `--allow-domains`) through a **squid** sidecar (the `sandboxer-proxy` image, built
  beside the toolbox image; `config.ProxyImage()`) running a generated `squid.conf` — the binary is never in the
  network path. The config block is `egress` (`egress.enabled` = false drops to a direct proxy; default on).
  Disable with `SANDBOXER_NO_EGRESS=1`.
- **Agent registry** (`internal/registry/registry.json`): the single-source catalog of agents — embedded in the
  binary AND consumed by the Nix flake (`llm-agents.nix`). Edit the JSON, never duplicate it. An agent may
  declare `resume` (argv, e.g. `claude --continue`) and `resumePick` (the interactive picker, used when
  several panes ran the agent in one directory): the session restore relaunches it in panes that recorded
  the agent at capture (opt-out: profile `autoResume = false` / `SANDBOXER_NO_RESUME=1`). Each agent's
  `seed` entries name its host-home config paths (with skip lists for transcripts/caches): a profile with
  `hostConfigs = true` (scaffold default) COPIES those into the sandbox home as a per-file merge (missing
  files added on every create/enter/exec; existing files never overwritten) — never a mount, never written
  back, an in-sandbox login/logout/edit always wins (`sandbox.SeedHome`) — AND passes through the agents'
  `authEnv` vars set on the host (cli `hostAuthEnv` → RunOpts.AuthEnv; part of the session hash). Claude's
  `.claude/.credentials.json` is seed-SKIPPED: rotating refresh tokens die as copies (401) or hijack the
  host session — subscription auth = `claude setup-token` + export CLAUDE_CODE_OAUTH_TOKEN.
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
