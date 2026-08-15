# sandboxer — agent guide

Config-driven, multi-agent dev sandboxes, each a real microVM (a Go CLI). This file is the agent-facing entry point.
The generalizable engineering conventions live in vendored **praktik skills** (below) — stated once there and
never restated here (`proc-skill-single-source`). This file holds only the delta: which skills are active, this
repo's parameters/deviations, and knowledge no skill owns.

## Skills (authoritative — vendored from praktik)

The committed manifest is `praktik.toml` (the desired set). The lock and the materialized copies
(`.praktik/`, `.claude/skills/`) are gitignored and regenerated locally by `praktik sync`
([praktik](https://github.com/irasikhin/praktik) is optional author-tooling — not required to build or
contribute; the same conventions are also enforced by `.golangci.yml` and CI).

<!-- praktik:skills BEGIN -->
arch-go-cli, build-go-tooling, ci-github-binary-release, flow-incremental-pr,
flow-root-cause, lang-go-error-handling, lang-go-style, lang-go-testing,
proc-branch-pr-workflow, proc-conventional-commits, proc-doc-as-code,
proc-repo-hygiene, proc-security-posture, proc-skill-single-source
<!-- praktik:skills END -->

These own the generalizable rules — Go architecture, style, errors, testing, build/lint, binary release,
commits, PR flow, security posture. **Do NOT restate them here or in CONTRIBUTING.md — link instead.**

## This repo: parameters & deviations

Fills for the skills' placeholders, and the local choices that differ from the kpass reference `arch-go-cli`
was extracted from:

- **Module / app:** `github.com/irasikhin/sandboxer`; binary `sandboxer`; Go **1.25.10** (the floor is a security floor — CI runs govulncheck on the toolchain go.mod selects, so a reachable stdlib CVE is fixed by RAISING this: go1.24 carried GO-2026-4601/4602, and 1.25.8 became reachable-vulnerable the moment the microVM backend started calling `net.Resolver.LookupHost` — GO-2026-4971, fixed in 1.25.10).
- **CLI parser = `spf13/cobra`** — `arch-go-cli` is parser-agnostic; the kpass reference uses `alecthomas/kong`.
  Commands are `*cobra.Command` factories that self-register via `register()` in each command file's `init()`;
  the root is assembled from `commandFactories` in `internal/cli/cli.go` (no central command list). The
  `Run(args, stdin, stdout, stderr) int` stdio seam is exactly as `arch-go-cli` describes.
- **Config = NIX** (kpass uses TOML): `sandboxer.nix` at the project root, evaluated to JSON via host
  `nix-instantiate --eval --strict --json` under restrict-eval (`config.EvalConfig`; nix on the host is a
  HARD requirement of the CLI — the toolbox image is built with host nix too). Scalars resolve flag →
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
  branch (the CLI is synchronous). `exitErr{code}` (same file) is the second typed error: enter/exec wrap the
  child's non-zero exit code in it and `Run` passes the code through as the process exit code, printing nothing.
- **Lint:** `.golangci.yml` already matches `build-go-tooling`'s linter set verbatim. CI additionally enforces
  a **90% coverage floor** (the project-set value `lang-go-testing` / `ci-github-binary-release` refer to).
- **Release:** Linux **amd64 + arm64 only** today (no darwin matrix yet); `version` is hardcoded in
  `nix/package.nix` and bumped by `scripts/release.sh` — otherwise as `ci-github-binary-release`.

## Repo-only knowledge (owned by no skill)

- **Isolation backend** (`internal/backend`): ONE backend — `backend = "microsandbox"` (msb), a real microVM
  on libkrun (KVM/HVF; guest runs as uid 0 — the VM is the boundary, and virtio-fs writes land host-user-owned).
  The docker/podman container backend and the smolvm `microvm` runner were REMOVED (retired values get
  migration errors in `ValidateBackend`; the `vmRunner` seam went with them). Layout: `msb.go` = the argv
  dialect (pure builders, golden-tested without a hypervisor: create/exec/run, `msbNetworkArgs`,
  `--secret`/auth-env, preflights — /tmp shares, file mounts, fractional limits) + the msb image-store handoff;
  `vm_session.go` = the lifecycle over the pure `planSession` policy + sweeps; `vm_state.go` = the host-side
  records at `<state>/machines/microsandbox/<name>.json` (SOURCE OF TRUTH; msb labels are discoverability only,
  and the subdir name is load-bearing — old machines must still be found); `vm_image.go` = the build-tar store
  `<state>/images/<name>.tar` + `.sha256` (the content id is the freshness authority). msb's egress rules are
  name-bound (`*.domain` covers the domain and subdomains, raw IPs refused); its `--secret` DLP
  mode is opt-in behind `SANDBOXER_MSB_SECRETS=1` (unverified substitution + boot-time binding). Nested
  containers run natively against the guest kernel — no seccomp/subid machinery, no opt-in. macOS (HVF) and
  Windows/WSL2 compile but are NOT live-verified. See `docs/microvm.md`.
  sandboxer is a
  HOST tool — its binary is NOT baked into the toolbox image, so it is normally absent inside the sandbox;
  `PersistentPreRunE` in `cli.go` is a belt-and-suspenders **deny-all** (every command refuses when
  `SANDBOXER_IN_CONTAINER` is set, injected per-run by `msbCommonArgs`).
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
  already checked out in a worktree OF THE USER'S is ADOPTED (git allows one worktree per branch) — shared at
  its host path AND symlinked into the sandbox at `<slug>/<branch>/<repo>` (`Source.Link`), because the sandbox
  dir is the guest's workdir and a source reachable only elsewhere on the host is invisible from inside;
  `include` is honored on an adopted source exactly as on a managed one. Two checkouts are REFUSED, not adopted
  (`checkAdoptable`): the repo's OWN checkout (`.git` is a real dir → git would enter the sandbox, and the
  agent would edit the tree you work in) and one owned by another sandbox (host-wide via `Projects()` — two
  sandboxes must not share a tree). Also not adopted: a set-aside `_detached/` checkout
  (re-attached to its managed path instead) or a stale registration whose dir was hand-deleted (pruned, checked
  out fresh — `rm -rf ./sandboxes` self-heals on the next enter; `_meta/<slug>.gen` flips the session hash so
  the pre-deletion session machine is rebuilt, not reused with dead mounts). Trees are narrowed by
  `include` — a list of DIRECTORIES (literal anchored paths or ant-style directory patterns: `/services/*/`,
  `**/proto/` — a whole `**` segment matches any depth, expanded on disk per mount computation, zero matches =
  error) that narrows the guest's MOUNT SET, never the worktree (the host tree is always a complete
  checkout, so an IDE can open it; negations/unanchored paths are rejected, and patterns match directories
  only, never files — a mount names a path, not a file set; see `docs/view-mounts-design.md`). srcs is ALWAYS explicit — an empty list is rejected;
  the scaffolded config seeds `srcs = [{src = "."; branch = "feat/<name>";}]`. Relative src paths
  resolve against the PROJECT ROOT (not the profile file's dir). **Git does not enter the sandbox unless a
  source opts in**: no git-dir shares by default, no `GIT_CONFIG_*` — the
  MOUNT SET is the wall (`sandbox.Mounts` decides it): unnarrowed = one stable rw mount of `<slug>/` (plus
  adopted paths), so srcs edits are picked up by every enter/exec (a LIVE session sees them immediately);
  narrowed = `<slug>/` is NOT mounted at all (the host worktrees under it are complete — that absence IS the
  boundary) and each include dir is mounted rw at its own path. Commits happen on the host. Resolved sources are recorded at `_meta/<slug>.srcs.json`; a dropped source's worktree is
  REMOVED when clean (branch kept) and moved to `_detached/` only when it holds uncommitted work (a worktree in
  detached-HEAD state is left in place, and a non-worktree dir with content is renamed aside, never
  deleted). Teardown removes managed worktrees only and KEEPS branches (`recreate --full`
  deletes just the ones sandboxer minted — recorded per source). **git-only:** a non-git source is rejected
  with an init hint; non-git trees come in via `extraMounts`.
  **Opt-in git share** (`config.Src.Git` → `Source.Git`/`GitDir` → `sandbox.GitMounts` → `RunOpts.GitMounts`):
  per source, `git = "ro"|"rw"` shares the repo's COMMON git dir (`worktree.Detect`'s second return, recorded
  at resolve time — the src may itself be a linked worktree) identity-mapped at its own host path. That
  identity mapping IS the mechanism: a worktree's `.git` is a pointer file holding an ABSOLUTE host path, so
  mounting the dir where it already lives makes it resolve in the guest with no rewriting (which would break
  the host's view of the same file) and keeps `git worktree prune` from unregistering the host's worktree.
  `git` + `include` = HARD ERROR (`config.ValidateGit`): history carries the excluded files back in.
  `rw` also hands over `.git/hooks`+`.git/config` = code the HOST's git later runs — documented in
  SECURITY.md, not blocked. `SANDBOXER_NO_GIT=1` = kill switch (applied in `target.mounts`, like `noEgress`).
  The share is in the create argv → session hash, but deliberately NOT in MountGen (a git dir is not
  recreated under a live session). The image's `git` wrapper only fires when the pointer is unresolvable,
  so a shared source needs no guest-side change.
  **Remote srcs** (`internal/worktree/remote.go`): a `src` that is a git URL
  (`IsRemoteURL`: https/ssh/git/file scheme or scp-like `git@host:path`) is CLONED once into
  `<stateDir>/_remotes/<name>-<hash>/` (detached HEAD so every branch is free for `worktree add`), then treated
  as a normal local repo — `RepoRoot` = the cache. Clone-once: `resolveSrcs` clones if absent; `recreate`
  re-fetches (`RefreshRemotes` → `worktree.Fetch`, ff-safe). `branch:` on a remote → `PrepareBranch` checks out
  `origin/<branch>`. The cache is kept across teardown (shared, like a local repo), wiped by `clean`.
- **Egress** (`backend.msbNetworkArgs`): outbound traffic is restricted to an allowlist
  (`egress.allowedDomains` / `--allow-domains`) by msb's NAME-BOUND policy engine — the machine boots
  `--no-net` (default deny) + `--net-rule allow@*.domain:tcp:80,allow@*.domain:tcp:443` per domain (domain +
  subdomains, raw IPs refused; empty list = fully offline, a valid state). No sidecar — the
  `sandboxer-proxy` image no longer exists and the binary is never in the network path. `egress.proxy` =
  proxy-delegated mode: open network + guest HTTP(S)_PROXY env, loopback rewritten to
  `host.microsandbox.internal` + `allow@public,allow@host:tcp:<port>` (proxy alongside an allowlist = the
  proxy enforces it — warning, not error). The policy is in the create argv → session hash. The config block
  is `egress` (`egress.enabled` = false = open network; default on). Disable with `SANDBOXER_NO_EGRESS=1`.
- **Agent registry** (`internal/registry/registry.json`): the single-source catalog of agents — embedded in the
  binary AND consumed by both flakes (each agent's `nixPackage` is a plain nixpkgs attr — prebuilt from
  cache.nixos.org; pi alone is vendored at `internal/toolbox/assets/pi/`, grafted into pkgs by an overlay in
  both flakes). Edit the JSON, never duplicate it. An agent may
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
- **pi packages** (`internal/toolbox/assets/pi-orchestrator/` + `internal/sandbox/pipkgs.go`): pi loads
  extensions/skills/prompts from the `packages` list in `~/.pi/agent/settings.json` — its ONLY global
  config (no /etc-level file), which is why registration is host-side against the sandbox home rather
  than something the image can ship. `pi-agent-orchestrator` is vendored like pi (npm tarball + generated
  lockfile; devDependencies AND peerDependencies stripped in the src prep — dist/ ships prebuilt and pi
  supplies the peer packages to extensions itself via an alias map, so stripping them takes the build from
  368 lock entries to 11), grafted by the overlay in both flakes, and images.nix links it at the STABLE
  path `/etc/sandboxer/pi-packages/agent-orchestrator` (the settings file outlives image bumps, so it must
  never name a store path; the literal is duplicated in `sandbox.BakedPiPackages` — keep them in sync).
  `EnsurePiPackages` MERGES the entry on create/enter/exec (`prepareHome`, AFTER SeedHome so a seeded
  host settings.json is the file we merge into), dedupes both spellings pi accepts (string and
  `{source}`), and leaves an unparsable settings.json alone. Opt out: `piPackages = false` /
  `SANDBOXER_NO_PI_PACKAGES=1`.
- **Toolbox image** (`internal/toolbox` + flake `dockerTools.buildLayeredImage`): the OCI image with the agents
  baked in; the stock default is PREBUILT — `ghcr.io/irasikhin/sandboxer-toolbox:latest`, pushed by
  `.github/workflows/image.yml` (nightly + per v* tag; msb pulls it on first create, `image pull` refreshes) —
  while `var-` variants and offline hosts build with HOST nix (`toolbox.BuildImageHostNix` — no builder
  container) into the tar store, then `msb load`-ed; `nix run .#build-image` is the maintainer equivalent.
- **Integration tests** (`internal/itest`, `//go:build integration`): drive a real msb on KVM/HVF and skip
  cleanly when prerequisites are missing (no msb, no /dev/kvm); run via `scripts/itest.sh`. Excluded from the
  coverage gate; ci.yml runs the msb slice on KVM-capable runners. (The general test conventions are
  `lang-go-testing`; this is only the harness delta.)

## Working in this repo

- Branch off `main`; land via PR; stage explicit paths, never `git add -A` (`flow-incremental-pr`,
  `proc-branch-pr-workflow`).
- Green gate before commit: `gofmt` + `go vet` + `golangci-lint run` + `go test ./...` (`build-go-tooling`).
- Human contributor runbook (local setup, integration suite, release steps) lives in `CONTRIBUTING.md`.
