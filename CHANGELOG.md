# Changelog

All notable changes to this project are documented here.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.93.1] — 2026-09-02

### Fixed

- enable DNS on the nested podman default network (fbf9aee)


## [0.93.0] — 2026-09-01

### Added

- add limits.disk resource cap (SANDBOXER_DISK env) (8dd64a4)
- size the microVM root disk via --root-disk (default 20G) (3413063)

### Docs

- document limits.disk / --root-disk (20G sparse default) (07b2913)


## [0.92.0] — 2026-08-31

### Added

- color the narration, warnings and errors (45081ea)

### Docs

- record the orchestrator/deepseek-flash division of labor (05d1c99)


## [0.91.0] — 2026-08-28

### Added

- ship jj (Jujutsu) in the toolbox image (68923af)


## [0.90.0] — 2026-08-22

### Added

- ship the full base userland — and fix three things it exposed (4a9603b)


## [0.89.0] — 2026-08-21

### Added

- ship findutils — `find` and `xargs` were missing (d710cdf)


## [0.88.4] — 2026-08-21

### Fixed

- validate a profile the way the commands that run it do (46bb1c4)


## [0.88.3] — 2026-08-21

### Fixed

- report the forwards the MACHINE has, not the ones the config wants (a869617)


## [0.88.2] — 2026-08-21

### Fixed

- say when a published port is not in the running machine (b389f97)


## [0.88.1] — 2026-08-17

### Fixed

- pin the dsh launcher's interpreter, not /usr/bin/env (06ed34d)


## [0.88.0] — 2026-08-17

### Added

- SANDBOXER_PORTS, and dsh's web UI binds for the forward in-sandbox (52e48d8)


## [0.87.0] — 2026-08-17

### Added

- publish guest ports so a sandbox's web UI opens on the host (d69f550)


## [0.86.0] — 2026-08-17

### Added

- bake DeepSeek Harness (dsh) into the toolbox (9e888a2)


## [0.85.0] — 2026-08-15

### Added

- bake pi's agent orchestrator and enable it by default (3bbc817)


## [0.84.0] — 2026-08-14

### ⚠ Breaking changes

- drop aider, and make `image pull` actually refresh (b780779)

### Added

- drop aider, and make `image pull` actually refresh (b780779)

### Tests

- pin the guard's refusal on an image that ships git (04cbe53)


## [0.83.0] — 2026-08-14

### Added

- bake a comparison pack into the toolbox (diff, patch, difft, dyff) (34ea6ca)
- source-code pack + fuller python batteries with uv as the way out (7a4c22f)

### Fixed

- tell a bool `git = true` which mode it wants, and cover the share live (158abac)


## [0.82.0] — 2026-08-14

### Added

- per-source opt-in git-dir share (git = "ro" / "rw") (5217c8e)


## [0.81.0] — 2026-08-13

### Added

- bake the LLM-agent CLI batteries into the base image (#16) (a5e7d8c)

### Fixed

- detaching must not hinge on one key that desktops steal (c46ca35)


## [0.80.0] — 2026-08-13

### Added

- serve a docker-compatible podman socket for testcontainers (#15) (01686f7)
- bring the guest podman socket up at machine boot (78cf824)


## [0.79.0] — 2026-08-12

### Added

- guard git in the guest against 'repairing' a managed worktree (0d4f58f)


## [0.78.2] — 2026-08-12

### Fixed

- materialize sets a squatting non-worktree dir aside first (d5565f0)


## [0.78.1] — 2026-08-12

### Fixed

- a standalone repo squatting a managed path is set aside, not moved (763b35d)


## [0.78.0] — 2026-08-11

### Added

- flag an msb older than the verified release (f28f267)

### Fixed

- create validates the whole runtime before writing any state (9950ad3)


## [0.77.0] — 2026-08-11

### Added

- per-profile prebuilt image — image.ref (618340c)


## [0.76.1] — 2026-08-11

### Fixed

- pull the prebuilt image explicitly at ensure, with retries (2049037)

### CI

- skopeo needs --insecure-policy on a bare runner; self-test on edit (032d7c5)
- dest-creds instead of login; surface push errors as annotations (18a70a9)
- override the runner's v1 registries.conf — skopeo refuses it (7c6f7a0)


## [0.76.0] — 2026-08-10

### ⚠ Breaking changes

- the combined wall — allowlist stays enforced beside a proxy (e1531f2)
  with egress enabled and egress.proxy set, the VM network is no longer open — direct traffic outside egress.allowedDomains is refused. Session hashes flip, so existing machines recreate on the next enter.

### Added

- default to the prebuilt GHCR toolbox image; add `image pull` (bedfde3)
- the combined wall — allowlist stays enforced beside a proxy (e1531f2)

### Docs

- prebuilt-image flow — pull first, build for variants/offline (66c5104)

### CI

- re-run the flaky -race test leg (f1efc94)
- publish the prebuilt toolbox image to GHCR (nightly + release tags) (d77b739)

### Chores

- sweep the post-migration comment and lint tails (68d8892)


## [0.75.0] — 2026-08-10

### ⚠ Breaking changes

- agents from nixpkgs, vendor pi, drop llm-agents (146709b)
  image.llmAgentsRev and --llm-agents-rev are removed — the agents ride image.nixpkgsRev now (a removed-key hint says so).

### Added

- agents from nixpkgs, vendor pi, drop llm-agents (146709b)


## [0.74.0] — 2026-08-10

### ⚠ Breaking changes

- drop the docker/podman container backend (2653119)
  the docker/podman container backend is removed. backend = "docker" | "podman" | "auto" | "" no longer resolves — set backend = "microsandbox" (or "microvm"). SANDBOXER_ENGINE is gone, and the egress squid sidecar (with its SANDBOXER_PROXY_IMAGE image) no longer exists; egress is enforced by the microVM runner itself.
- drop the smolvm runner — microsandbox is the only backend (2a4df60)
  the "microvm" (smolvm) backend is removed — set backend = "microsandbox". SANDBOXER_SMOLVM is no longer read, and backend.SmolvmStatus/itest.Smolvm are gone. The docker/podman container backend is removed in this same release.

### Added

- drop the docker/podman container backend (2653119)
- retire the container-era config keys and validation (f92d9c8)
- collapse the CLI onto the microVM backends (8fc385a)
- drop the smolvm runner — microsandbox is the only backend (2a4df60)

### Fixed

- pass the pinned flake-input revs to the host-nix build (702daa1)
- engine-less tests must silence SANDBOXER_MSB, not just PATH (b18068d)

### Refactored

- drop the container image builder (aaa358a)

### Docs

- rewrite the container-era docs for the msb-only backend (955425a)
- mark the spike/verification records as historical (40d12cc)

### Build

- drop the egress proxy image (0d911c8)
- recompute vendorHash — compose's yaml.v3 left the import graph (9566338)
- drop the smolvm package, app and devShell tool (91f9e3a)

### CI

- drop the proxyImage smoke builds — the flake output is gone (064ef4f)
- drop the smolvm e2e legs; restore the full Jenkins sweep (9b9cfd2)


## [0.73.7] — 2026-08-10

### Fixed

- bake /etc/passwd + /etc/group into the toolbox image (9557375)
- survive the real toolbox image — getent probe, Hub's new CDN (605461b)
- probe host DNS in-process, not via getent (b190337)
- read image freshness from the build tar, not msb's cached digest (b4f0041)

### CI

- msb legs replace docker — itest on KVM, Jenkins without dind (7778138)
- node-local nix cache + serialized heavy builds — the pod OOMed (172f4d3)
- scope the run to the msb real-engine suite (32673c6)

### Chores

- migrate praxis -> praktik author-tooling (4689d29)


## [0.73.6] — 2026-08-10

### Fixed

- spell the proxy host rule in the 0.6.x-wide rule vocabulary (19312a6)


## [0.73.5] — 2026-08-10

### Fixed

- reach a host-loopback egress proxy from the guest (3e83739)


## [0.73.4] — 2026-08-10

### Fixed

- drop -i from exec/run argv — not in msb's grammar (84b08f7)


## [0.73.3] — 2026-08-10

### Fixed

- decompress the store tar for msb load — gzip outer archive (e8db875)


## [0.73.2] — 2026-08-09

### Fixed

- build the image tar inside the store — EXDEV on tmpfs /tmp (cc5b903)


## [0.73.1] — 2026-08-09

### Fixed

- stop accepts multiple slugs/ids in one invocation (57be7fb)


## [0.73.0] — 2026-08-09

### Added

- build the toolbox image with host nix, resolve pins via host git (ecdec51)
- close the microVM parity gaps (4a4c867)

### Changed

- On a microVM backend, `limits.cpus` and `limits.memory` are now VALIDATED
  instead of silently coerced: a fractional `limits.cpus` (e.g. `"0.5"`) and an
  unparseable `limits.memory` are hard errors, where they previously rounded up
  to a whole vCPU and fell back to the 4 GiB default. Container backends are
  unchanged. (4a4c867)

### Fixed

- harden the host-nix build and correct the image-rm comment (4ecdaa9)
- make the nested-container probe discriminating (postgres as a service) (8ec7dd2)

### Docs

- cite the measured nested-containers verification (b5bd254)
- scope the nested-containers claim to the measured guest (b4f6630)

### Chores

- gitignore the inter-agent comm files (d4d44f5)


## [0.72.0] — 2026-08-08

### Added

- run nested containers under a real seccomp profile (2c45e41)
- ship a docker CLI and compose inside the sandbox (8d71ddf)
- tell the user whether nested containers can actually work (370d7ba)

### Refactored

- fold the nested-seccomp gate into one predicate (0b9cd8e)

### Docs

- correct why old profile files are not pruned (7f91365)

### Tests

- assert the nested uid on a marker, not a bare digit (71ae4a0)


## [0.71.0] — 2026-08-07

### ⚠ Breaking changes

- link adopted sources into the sandbox, refuse unsafe adoption (d0f5e7f)
  a srcs entry naming a branch checked out in the repository itself, or in another sandbox, is an error instead of a silent adoption. Give that source its own branch.

### Fixed

- link adopted sources into the sandbox, refuse unsafe adoption (d0f5e7f)


## [0.70.0] — 2026-08-05

### Added

- resume commands for pi, codex, opencode, crush and aider (43434e5)

### Fixed

- tear the session down on whatever engine holds it (090945f)


## [0.69.4] — 2026-08-04

### Fixed

- seed ~/.tmux.conf so extended keys work without an image rebuild (d61d551)


## [0.69.3] — 2026-08-04

### Fixed

- enable tmux extended keys so Shift-Enter reaches the agent (aa907ee)


## [0.69.2] — 2026-08-04

### Refactored

- drop the llm-agents numtide binary cache (f92b96f)

### CI

- drop the stale binary-cache comment from the images job (75f960a)


## [0.69.1] — 2026-08-03

### Fixed

- surface microVM machines whose host-side record was lost (a9f9072)

### Build

- lead the changelog section with breaking changes (d7c424b)


## [0.69.0] — 2026-08-03

### Added

- track latest agent revs by default — image build auto-updates (785455c)

### CI

- fix the smolvm canary and stop a skipped job reporting green (341e344)


## [0.68.0] — 2026-08-03

### Fixed

- an empty allowedDomains means deny-all, not the defaults (a6064e6)
- validate the backend at create, not only at enter/exec (43f8e89)

### Docs

- correct the smolvm allowlist grammar, record the EINVAL breakage (099417d)
- record the 2026-07-31 four-backend verification pass (b9a546b)

### Tests

- cover the exit-code passthrough of exec and enter (488d060)


## [0.67.0] — 2026-08-03

### Added

- pass the child's exit code through as sandboxer's own (3ce044d)
- --strict CI gate and a git prerequisite row (11f76c1)
- capture the setup script's output to _logs (e93ebb4)
- --json output for list, show and doctor (d25888a)
- -q/--quiet for enter and exec (30a7e40)
- validate runs the static semantic checks (f36415b)

### Fixed

- drop the never-populated EXIT/SEC/RESULT columns (5663df8)

### Build

- refresh vendorHash for the x/sys bump (088d93f)

### CI

- bump actions/setup-go from 5 to 7 (5c7a9d5)

### Chores

- bump golang.org/x/sys (c0e9e2e)

### Style

- drop unnecessary io.Writer conversions (fc4c4d0)


## [0.66.0] — 2026-07-31

### Added

- full project paths and a host-wide id per sandbox (ca0c92f)


## [0.65.0] — 2026-07-30

### Added

- make the listing host-wide across every project (86a1d5f)

### CI

- keep the golangci-lint pin rationale current with the Go floor (9ea962a)
- make the lint job deterministic (skip the golangci-lint cache) (7eef8c2)


## [0.64.0] — 2026-07-28

### Added

- microsandbox as a second microVM runner (d0a2774)

### Fixed

- raise the Go floor to 1.25.10 (372a0b2)

### Tests

- cover the microVM failure paths (b7dfe86)

### CI

- run the microVM e2e for microsandbox too (878dcd9)


## [0.63.5] — 2026-07-27

### Fixed

- seed a nested-file config path (create parent dirs) (8919eab)


## [0.63.4] — 2026-07-27

### Fixed

- microvm image build completes (skip proxyImage, cache) (707fa49)


## [0.63.3] — 2026-07-27

### Fixed

- microvm image build reaches a loopback host proxy (e5fed40)


## [0.63.2] — 2026-07-27

### Fixed

- make the microvm toolbox image build actually work (d00e424)


## [0.63.1] — 2026-07-27

### Fixed

- let microvm egress.proxy and allowlist coexist (was a hard error) (d8fcdba)


## [0.63.0] — 2026-07-27

### Added

- consistent microvm egress-proxy support (ec34f9e)

### Fixed

- microvm parity for clean/doctor/list and profile.json (33052a4)

### Docs

- microsandbox vs smolvm spike report (a5225ea)


## [0.62.1] — 2026-07-26

### Fixed

- image build/rm honor the profile's backend (microvm store) (1e7125b)


## [0.62.0] — 2026-07-26

### Added

- smolvm argv builders for the microvm backend (991cce8)
- microvm session lifecycle and state (0e02944)
- microvm egress via the smolvm allowlist (b864878)
- enable backend = "microvm" end to end (f31d9eb)
- build the toolbox image inside a microVM + image store (b8115b8)

### Refactored

- extract session policy and a guest-exec seam (bc82aec)

### Docs

- backend guide, e2e checklist, Windows/WSL2, CI workflow (d9194ce)
- microvm spike report (linux leg — GO) (34b9c84)

### Tests

- real microVM e2e suite (smolvm) (3a0d621)

### Build

- package smolvm and expose it from the flake (028eb49)


## [0.61.0] — 2026-07-26

### Added

- multi-uid nested podman on a podman engine (12ce65f)
- allow registry CDN blobs by default (cloudfront.net, public.ecr.aws) (6af84b8)

### Fixed

- compose renders nestedContainers profiles correctly (3507be5)

### Docs

- multi-uid nested podman, registry CDN allowlist, migrate runbook (9d5b979)


## [0.60.0] — 2026-07-23

### Added

- group sandbox worktrees by branch, not repo (3e8796b)


## [0.59.0] — 2026-07-23

### Added

- relaunch recorded agents when restoring a session (8b13d5d)
- open the conversation picker for same-dir agent panes (51533e2)


## [0.58.1] — 2026-07-21

### Fixed

- read tmux base-index from the created window, not up front (27e46fb)


## [0.58.0] — 2026-07-21

### Added

- survive container replacement — capture and restore the tmux layout (4410d10)

### Fixed

- raise the Go floor to 1.25.8 for two reachable stdlib CVEs (5526560)

### CI

- gate the engine-level integration tests and scan for vulnerabilities (96fdec8)
- take the deployment specifics out of a public file (66b40bb)
- pin golangci-lint to a build newer than the Go floor (31ca375)


## [0.57.0] — 2026-07-20

### Added

- name what moved in a stale session, and offer the rebuild (aee8c3f)

### Fixed

- attach to a stale running session instead of a one-shot container (6adf38d)
- scope auth env to the process, not the session container (b9002bc)
- never prompt without a real terminal, and say the diff once (af64d10)

### Tests

- fix the bit-rotted agent auth-env integration test (eac3995)


## [0.56.3] — 2026-07-20

### Fixed

- let the builder inherit the host's proxy (e781dde)


## [0.56.2] — 2026-07-20

### Fixed

- tell detach and exit apart in the session banner (8a7ed18)


## [0.56.1] — 2026-07-20

### Fixed

- stop promising persistence before choosing the container (4c6134e)


## [0.56.0] — 2026-07-20

### Added

- a Catppuccin status bar and tmux defaults that fit agent TUIs (061121a)
- support remote git repos as sources (src = "<url>") (142ff78)

### Fixed

- surface the engine's stderr on failure (30d00bd)

### Build

- scope the nixfmt hook away from examples/ (803106a)

### Chores

- repo-wide cleanup — doc drift, dead code, hygiene (b15e333)


## [0.55.0] — 2026-07-20

### Added

- use Ctrl-Space as the tmux prefix (was Ctrl-b) (631ec6b)


## [0.54.0] — 2026-07-20

### Added

- ant-style directory patterns in include (18b22b6)

### Docs

- include patterns — design rationale, README, examples (763f113)


## [0.53.2] — 2026-07-19

### Fixed

- serialize session converge to fix the first-enter egress race (bcfd59c)
- refuse an in-place worktreesDir change; route to recreate (4eb60c6)


## [0.53.1] — 2026-07-19

### Fixed

- reject "." and ".." sandbox names (rm .. deleted the project) (ac3c42e)
- validate allowlist domains as hostnames (squid.conf injection) (8504fc2)
- signpost the open-network egress state (was shown as "off") (060ff3d)
- refuse to abandon un-merged commits without --force (7835419)
- refuse to discard uncommitted work; fix --full branch help (55e3078)
- recognize a wrapped silentErr, surface profile-marshal errors (e888a35)

### Docs

- fix post-migration drift; note the restrict-eval getEnv caveat (55f525c)

### CI

- verify the module graph (go mod verify) (32bf255)


## [0.53.0] — 2026-07-18

### Added

- add an `sb` shorthand and bake python batteries into the image (192c015)

### Fixed

- reject a symlinked include that escapes the worktree (7e0f46a)

### Docs

- align package comments with the view-mount model (c1a5f87)


## [0.52.3] — 2026-07-17

### Fixed

- accept an include directory without a trailing slash (f47b259)

### Tests

- pin nested parent+child include behavior (235ab4b)


## [0.52.2] — 2026-07-17

### Fixed

- survive ext4 inode reuse in the mount fingerprint (f3ddda6)


## [0.52.1] — 2026-07-17

### Fixed

- rebuild the session when a mounted view directory is recreated (af1163f)


## [0.52.0] — 2026-07-17

### Added

- narrow the container's mounts, not the host worktree (50ecded)


## [0.51.0] — 2026-07-17

### Added

- enter attaches an in-container tmux again; drop zellij from the image (6e857dd)


## [0.50.0] — 2026-07-16

### Added

- pass host auth env through under hostConfigs; stop seeding claude's rotating OAuth file (2b4eb74)


## [0.49.1] — 2026-07-16

### Fixed

- seed hostConfigs as a per-file merge, not a whole-path skip (956de65)


## [0.49.0] — 2026-07-16

### Added

- hostConfigs — seed the sandbox home from the host's agent configs (2d4f6e5)

### Fixed

- make _detached recoverable and rm -rf ./sandboxes self-healing (3f66e26)


## [0.48.0] — 2026-07-16

### Added

- reset — re-base a sandbox's source branches onto the merged base (f58c553)


## [0.47.1] — 2026-07-16

### Fixed

- define the images once, not twice (9e75ad4)

### Docs

- fix 'path' source-selector help — repo directory, not worktree-dir basename (d947c42)


## [0.47.0] — 2026-07-16

### Added

- opt-in nested rootless podman in the sandbox (aa9e2a3)


## [0.46.0] — 2026-07-16

### Added

- enter never tears down a running stale session by default (3b8bbac)
- path <slug> <source> selects one source worktree (423bfac)


## [0.45.0] — 2026-07-16

### Added

- default worktrees to ./sandboxes in the project, auto-gitignored (ce30111)


## [0.44.0] — 2026-07-16

### Added

- group worktrees by repo; relocatable root via worktreesDir (f1e57f9)


## [0.43.0] — 2026-07-16

### Added

- clean --detached — sweep set-aside dropped sources alone (7f075d3)


## [0.42.0] — 2026-07-16

### Added

- explicit branch per source; worktrees live beside the project (a12d4d8)

### Fixed

- sticky recorded branch — a default rename must not reset sandboxes (b98ca74)

### Docs

- record the sticky-branch invariant (3632763)


## [0.41.0] — 2026-07-16

### Added

- flatten the image schema — packages/files/env + a plain overlay file (9c3d843)


## [0.40.0] — 2026-07-16

### Added

- print sandbox worktree paths with `sandboxer path` (ccdded0)


## [0.39.0] — 2026-07-16

### Added

- java 25 in the base image; remove the agents credential passthrough (7d42b96)


## [0.38.0] — 2026-07-16

### Added

- everyday toolchain + rootless nested podman in the base image (460d250)


## [0.37.0] — 2026-07-16

### Added

- list the connected sources on every enter (67b7b99)
- drop the -sb suffix — reuse existing feat/<slug> branches (1bb0e22)


## [0.36.0] — 2026-07-16

### Added

- show the binary version on every create/enter/exec banner (4d6b055)


## [0.35.1] — 2026-07-16

### Fixed

- announce each slow step of session convergence (a2eb14b)


## [0.35.0] — 2026-07-15

### Added

- stop managing tmux — multiplexing is the user's own (196c311)

### Fixed

- default the container to a UTF-8 locale (85e78a4)


## [0.34.0] — 2026-07-15

### Added

- rename the network block to egress with an enabled toggle (57db7ed)

### Fixed

- reach platform.claude.com by default so Claude Code connects (cd626d8)


## [0.33.1] — 2026-07-15

### Fixed

- never let a substituter wedge the image build (a0c719c)


## [0.33.0] — 2026-07-15

### Added

- nix config — sandboxer.nix replaces YAML, host nix required (eafccf8)


## [0.32.0] — 2026-07-15

### Added

- srcs is always explicit — no implicit current-dir source (c11af59)
- profiles live in one config file — drop the store and -f dirs (d2db9c9)

### Fixed

- name the real cause when srcs is empty; warn on empty include (7ccb3c4)


## [0.31.0] — 2026-07-15

### Added

- name sandbox branches feat/<slug>-sb instead of sandbox/<slug> (6bc3f71)
- relocate config to sandboxer.yaml + sandboxer-image.nix at repo root (556dfca)
- srcs model — multi-repo sources, git never enters the container (102bc40)


## [0.30.0] — 2026-07-14

### Added

- accept --backend on create (4762980)
- config get/set/unset; move profile init/edit/validate to config (b40ec55)
- remove config inheritance — no defaults:, no global config (6a74cf0)

### Fixed

- retire copy-in-era help text and ghost commands (b0bb7c7)
- align enter's Use string, add enter/rm help, de-collide the profile-list default marker (f29de25)

### Refactored

- add comment-preserving yaml editor, key registry, LoadDocumentBytes (5394372)

### Docs

- fix stale examples, dedupe restated config docs, drop the PoC eval matrix (f821cde)


## [0.29.0] — 2026-07-13

### Refactored

- remove mcp: — the worktree redesign made it redundant (02d0cce)

### Docs

- drop stale copy-mode references left after the git-only migration (29a3a82)

### Tests

- drop obsolete copy-mode lifecycle integration tests (09cb6ab)


## [0.28.0] — 2026-07-13

### Added

- wire resource limits (memory/cpus/pids) (ccd9555)
- domain-routed upstream proxies + fold sidecar config into session freshness (df1319d)
- experimental macOS support — SANDBOXER_CONTAINER_USER escape hatch + setup docs (db2c33d)

### Fixed

- build the itest smoke image via nix, not `docker pull` from Docker Hub (69ea3f5)
- mount repo config/hooks read-only to close git-dir RCE (3a13c4a)
- update integration tests for removed Wall + new egress.Up signature (1e03950)

### Refactored

- remove the model: knob (8b05617)
- move proxy/noProxy under network: (0b521f7)
- remove agent: and agentProxy: (0d84165)
- git-only sandboxes — drop the copy-mode fallback (4e9d536)

### Docs

- drop references to the removed run command (14eaeaa)
- rewrite SECURITY.md + docs/architecture.md to match reality (3e85ebd)

### Chores

- prune dead resource/parallelism knobs (a7d19dd)
- drop dead launch-command + credential-dir surface (7264db3)


## [0.27.0] — 2026-07-09

### Added

- back sandboxes with git worktrees instead of copy-in (4a20eff)

### Fixed

- discover .sandboxer/config.yaml under --src, not just the cwd (a7d5cef)


## [0.26.3] — 2026-07-08

### Fixed

- make the Jenkins e2e build work on the homelab agent (94b7c65)
- make the Jenkins e2e resilient to throttled egress (01bb1fe)
- route Jenkins egress through the AmneziaWG proxy (91cfa3f)
- build both images + share tmp with dind so bind-mount tests pass (1b7d13f)
- retry the alpine pull (transient 503 through the egress proxy) (5d2b662)

### Docs

- correct the test-coverage audit and note the Jenkins e2e run (0296f5c)

### Tests

- repair rotted e2e suite for the squid egress path (688f7ed)
- add real-behaviour e2e for the squid proxy mechanism (0ef76e8)
- add real-engine e2e for agent env/home isolation and recreate (09a891a)
- skip live-internet egress tests where containers have no direct egress (afdc8c7)

### CI

- add container e2e pipeline for the homelab Jenkins (c0b6534)


## [0.26.2] — 2026-06-30

### Fixed

- correct stale proxy syntax in profile scaffold (2eff5fa)


## [0.26.1] — 2026-06-23

### Fixed

- wire llm-agents binary cache into the embedded flake (f8847a3)


## [0.26.0] — 2026-06-23

### Added

- single proxy URL + per-agent and global proxy (1a0997e)

### Fixed

- give the proxy sidecar the host-gateway alias (185ba71)


## [0.25.0] — 2026-06-19

### Added

- list profiles across project, global and store (aff1cfc)

### CI

- bump actions/checkout from 4 to 7 (1307929)


## [0.24.1] — 2026-06-18

### CI

- build proxy image every run, both images on tag release (a31fdc9)


## [0.24.0] — 2026-06-18

### Added

- host-only sandboxer — squid egress, XDG config/data split, CLI regroup (24ff79e)


## [0.23.0] — 2026-06-11

### Added

- A **direnv hook**: `sandboxer hook direnv` prints the active sandbox as
  host-shell exports (`SANDBOXER_SLUG`/`SANDBOXER_SRC`, plus the recorded
  `SANDBOXER_MODEL`/`SANDBOXER_BACKEND`/`SANDBOXER_ALLOW_DOMAINS`) for an
  `.envrc`, with a bundled `use_sandboxer` helper
  (`contrib/direnv/use_sandboxer.sh`) so an `.envrc` can `use sandboxer` and
  reload on `sandboxer use <slug>`. It is read-only — nothing is built or
  started on `cd` — and a no-op (exit 0) outside a sandboxer project or with no
  active sandbox, so it is safe to call unconditionally.
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
- **`sandboxer recreate [--full]`** — rebuild a sandbox from its profile in
  one step: the working copy, manifest and setup stamp are wiped (setup re-runs
  on the next enter) and deps are pulled fresh. The private agent home —
  logins, shell history — is preserved; `--full` wipes it too, making recreate
  equivalent to `rm` + `create`. (6050cf9)
- **Safe push**: `pull` records a signature of each rw origin in the manifest,
  and `push` — including the automatic copy-back after `enter`/`exec` — skips
  any origin edited on the host since that pull instead of silently
  overwriting it; `push --force` restores the wholesale overwrite. Also fixes
  the in-container `pull --force` self-destruct: an origin that IS the sandbox
  copy is kept, never copied onto itself. (af4d4ce)
- The current directory is always searched as an **implicit last root**, so a
  project-local dep needs no `roots:` stanza; explicit roots win the
  deterministic first-match. (1638993)
- **Agent context files**: `CLAUDE.md`, `AGENTS.md` and `.claude/` (those that
  exist in the project root) are copied read-only to the sandbox root, so
  agents see the project's instructions; a non-empty `context:` profile list
  replaces the default set. Refreshed on `pull`, never pushed back. (9490dc3)
- A **JSON Schema** for the profile (`schema/config.schema.json`), generated
  from the same Go structs the strict parser uses; the scaffolded config opens
  with a `yaml-language-server` header so editors flag typos before sandboxer
  runs. (1f16586)
- `doctor` (and `create`) warn when the repo's gitignore hides
  `.sandboxer/config.yaml` — a root-level `.sandboxer/` rule defeats the
  generated allowlist. (5d53d5c)

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
- Deps are vendored under **`<sandbox>/workspace/`** instead of the sandbox
  root, keeping the root free for the agent context files. Existing sandboxes
  keep the old flat layout (a pull prints a NOTE) — run
  `sandboxer recreate <slug>`; `setup:` scripts that `cd` into a dep need the
  `workspace/` prefix. (fa602ec)
- `push` no longer overwrites an origin edited on the host since the last pull
  (use `push --force`). Manifests written by older versions carry no
  signatures, so the first push after upgrading reports skips — re-pull or
  `push --force` once. (af4d4ce)

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
[0.23.0]: https://github.com/irasikhin/sandboxer/compare/v0.22.1...v0.23.0
[0.24.0]: https://github.com/irasikhin/sandboxer/compare/v0.23.0...v0.24.0
[0.24.1]: https://github.com/irasikhin/sandboxer/compare/v0.24.0...v0.24.1
[0.25.0]: https://github.com/irasikhin/sandboxer/compare/v0.24.1...v0.25.0
[0.26.0]: https://github.com/irasikhin/sandboxer/compare/v0.25.0...v0.26.0
[0.26.1]: https://github.com/irasikhin/sandboxer/compare/v0.26.0...v0.26.1
[0.26.2]: https://github.com/irasikhin/sandboxer/compare/v0.26.1...v0.26.2
[0.26.3]: https://github.com/irasikhin/sandboxer/compare/v0.26.2...v0.26.3
[0.27.0]: https://github.com/irasikhin/sandboxer/compare/v0.26.3...v0.27.0
[0.28.0]: https://github.com/irasikhin/sandboxer/compare/v0.27.0...v0.28.0
[0.29.0]: https://github.com/irasikhin/sandboxer/compare/v0.28.0...v0.29.0
[0.30.0]: https://github.com/irasikhin/sandboxer/compare/v0.29.0...v0.30.0
[0.31.0]: https://github.com/irasikhin/sandboxer/compare/v0.30.0...v0.31.0
[0.32.0]: https://github.com/irasikhin/sandboxer/compare/v0.31.0...v0.32.0
[0.33.0]: https://github.com/irasikhin/sandboxer/compare/v0.32.0...v0.33.0
[0.33.1]: https://github.com/irasikhin/sandboxer/compare/v0.33.0...v0.33.1
[0.34.0]: https://github.com/irasikhin/sandboxer/compare/v0.33.1...v0.34.0
[0.35.0]: https://github.com/irasikhin/sandboxer/compare/v0.34.0...v0.35.0
[0.35.1]: https://github.com/irasikhin/sandboxer/compare/v0.35.0...v0.35.1
[0.36.0]: https://github.com/irasikhin/sandboxer/compare/v0.35.1...v0.36.0
[0.37.0]: https://github.com/irasikhin/sandboxer/compare/v0.36.0...v0.37.0
[0.38.0]: https://github.com/irasikhin/sandboxer/compare/v0.37.0...v0.38.0
[0.39.0]: https://github.com/irasikhin/sandboxer/compare/v0.38.0...v0.39.0
[0.40.0]: https://github.com/irasikhin/sandboxer/compare/v0.39.0...v0.40.0
[0.41.0]: https://github.com/irasikhin/sandboxer/compare/v0.40.0...v0.41.0
[0.42.0]: https://github.com/irasikhin/sandboxer/compare/v0.41.0...v0.42.0
[0.43.0]: https://github.com/irasikhin/sandboxer/compare/v0.42.0...v0.43.0
[0.44.0]: https://github.com/irasikhin/sandboxer/compare/v0.43.0...v0.44.0
[0.45.0]: https://github.com/irasikhin/sandboxer/compare/v0.44.0...v0.45.0
[0.46.0]: https://github.com/irasikhin/sandboxer/compare/v0.45.0...v0.46.0
[0.47.0]: https://github.com/irasikhin/sandboxer/compare/v0.46.0...v0.47.0
[0.47.1]: https://github.com/irasikhin/sandboxer/compare/v0.47.0...v0.47.1
[0.48.0]: https://github.com/irasikhin/sandboxer/compare/v0.47.1...v0.48.0
[0.49.0]: https://github.com/irasikhin/sandboxer/compare/v0.48.0...v0.49.0
[0.49.1]: https://github.com/irasikhin/sandboxer/compare/v0.49.0...v0.49.1
[0.50.0]: https://github.com/irasikhin/sandboxer/compare/v0.49.1...v0.50.0
[0.51.0]: https://github.com/irasikhin/sandboxer/compare/v0.50.0...v0.51.0
[0.52.0]: https://github.com/irasikhin/sandboxer/compare/v0.51.0...v0.52.0
[0.52.1]: https://github.com/irasikhin/sandboxer/compare/v0.52.0...v0.52.1
[0.52.2]: https://github.com/irasikhin/sandboxer/compare/v0.52.1...v0.52.2
[0.52.3]: https://github.com/irasikhin/sandboxer/compare/v0.52.2...v0.52.3
[0.53.0]: https://github.com/irasikhin/sandboxer/compare/v0.52.3...v0.53.0
[0.53.1]: https://github.com/irasikhin/sandboxer/compare/v0.53.0...v0.53.1
[0.53.2]: https://github.com/irasikhin/sandboxer/compare/v0.53.1...v0.53.2
[0.54.0]: https://github.com/irasikhin/sandboxer/compare/v0.53.2...v0.54.0
[0.55.0]: https://github.com/irasikhin/sandboxer/compare/v0.54.0...v0.55.0
[0.56.0]: https://github.com/irasikhin/sandboxer/compare/v0.55.0...v0.56.0
[0.56.1]: https://github.com/irasikhin/sandboxer/compare/v0.56.0...v0.56.1
[0.56.2]: https://github.com/irasikhin/sandboxer/compare/v0.56.1...v0.56.2
[0.56.3]: https://github.com/irasikhin/sandboxer/compare/v0.56.2...v0.56.3
[0.57.0]: https://github.com/irasikhin/sandboxer/compare/v0.56.3...v0.57.0
[0.58.0]: https://github.com/irasikhin/sandboxer/compare/v0.57.0...v0.58.0
[0.58.1]: https://github.com/irasikhin/sandboxer/compare/v0.58.0...v0.58.1
[0.59.0]: https://github.com/irasikhin/sandboxer/compare/v0.58.1...v0.59.0
[0.60.0]: https://github.com/irasikhin/sandboxer/compare/v0.59.0...v0.60.0
[0.61.0]: https://github.com/irasikhin/sandboxer/compare/v0.60.0...v0.61.0
[0.62.0]: https://github.com/irasikhin/sandboxer/compare/v0.61.0...v0.62.0
[0.62.1]: https://github.com/irasikhin/sandboxer/compare/v0.62.0...v0.62.1
[0.63.0]: https://github.com/irasikhin/sandboxer/compare/v0.62.1...v0.63.0
[0.63.1]: https://github.com/irasikhin/sandboxer/compare/v0.63.0...v0.63.1
[0.63.2]: https://github.com/irasikhin/sandboxer/compare/v0.63.1...v0.63.2
[0.63.3]: https://github.com/irasikhin/sandboxer/compare/v0.63.2...v0.63.3
[0.63.4]: https://github.com/irasikhin/sandboxer/compare/v0.63.3...v0.63.4
[0.63.5]: https://github.com/irasikhin/sandboxer/compare/v0.63.4...v0.63.5
[0.64.0]: https://github.com/irasikhin/sandboxer/compare/v0.63.5...v0.64.0
[0.65.0]: https://github.com/irasikhin/sandboxer/compare/v0.64.0...v0.65.0
[0.66.0]: https://github.com/irasikhin/sandboxer/compare/v0.65.0...v0.66.0
[0.67.0]: https://github.com/irasikhin/sandboxer/compare/v0.66.0...v0.67.0
[0.68.0]: https://github.com/irasikhin/sandboxer/compare/v0.67.0...v0.68.0
[0.69.0]: https://github.com/irasikhin/sandboxer/compare/v0.68.0...v0.69.0
[0.69.1]: https://github.com/irasikhin/sandboxer/compare/v0.69.0...v0.69.1
[0.69.2]: https://github.com/irasikhin/sandboxer/compare/v0.69.1...v0.69.2
[0.69.3]: https://github.com/irasikhin/sandboxer/compare/v0.69.2...v0.69.3
[0.69.4]: https://github.com/irasikhin/sandboxer/compare/v0.69.3...v0.69.4
[0.70.0]: https://github.com/irasikhin/sandboxer/compare/v0.69.4...v0.70.0
[0.71.0]: https://github.com/irasikhin/sandboxer/compare/v0.70.0...v0.71.0
[0.72.0]: https://github.com/irasikhin/sandboxer/compare/v0.71.0...v0.72.0
[0.73.0]: https://github.com/irasikhin/sandboxer/compare/v0.72.0...v0.73.0
[0.73.1]: https://github.com/irasikhin/sandboxer/compare/v0.73.0...v0.73.1
[0.73.2]: https://github.com/irasikhin/sandboxer/compare/v0.73.1...v0.73.2
[0.73.3]: https://github.com/irasikhin/sandboxer/compare/v0.73.2...v0.73.3
[0.73.4]: https://github.com/irasikhin/sandboxer/compare/v0.73.3...v0.73.4
[0.73.5]: https://github.com/irasikhin/sandboxer/compare/v0.73.4...v0.73.5
[0.73.6]: https://github.com/irasikhin/sandboxer/compare/v0.73.5...v0.73.6
[0.73.7]: https://github.com/irasikhin/sandboxer/compare/v0.73.6...v0.73.7
[0.74.0]: https://github.com/irasikhin/sandboxer/compare/v0.73.7...v0.74.0
[0.75.0]: https://github.com/irasikhin/sandboxer/compare/v0.74.0...v0.75.0
[0.76.0]: https://github.com/irasikhin/sandboxer/compare/v0.75.0...v0.76.0
[0.76.1]: https://github.com/irasikhin/sandboxer/compare/v0.76.0...v0.76.1
[0.77.0]: https://github.com/irasikhin/sandboxer/compare/v0.76.1...v0.77.0
[0.78.0]: https://github.com/irasikhin/sandboxer/compare/v0.77.0...v0.78.0
[0.78.1]: https://github.com/irasikhin/sandboxer/compare/v0.78.0...v0.78.1
[0.78.2]: https://github.com/irasikhin/sandboxer/compare/v0.78.1...v0.78.2
[0.79.0]: https://github.com/irasikhin/sandboxer/compare/v0.78.2...v0.79.0
[0.80.0]: https://github.com/irasikhin/sandboxer/compare/v0.79.0...v0.80.0
[0.81.0]: https://github.com/irasikhin/sandboxer/compare/v0.80.0...v0.81.0
[0.82.0]: https://github.com/irasikhin/sandboxer/compare/v0.81.0...v0.82.0
[0.83.0]: https://github.com/irasikhin/sandboxer/compare/v0.82.0...v0.83.0
[0.84.0]: https://github.com/irasikhin/sandboxer/compare/v0.83.0...v0.84.0
[0.85.0]: https://github.com/irasikhin/sandboxer/compare/v0.84.0...v0.85.0
[0.86.0]: https://github.com/irasikhin/sandboxer/compare/v0.85.0...v0.86.0
[0.87.0]: https://github.com/irasikhin/sandboxer/compare/v0.86.0...v0.87.0
[0.88.0]: https://github.com/irasikhin/sandboxer/compare/v0.87.0...v0.88.0
[0.88.1]: https://github.com/irasikhin/sandboxer/compare/v0.88.0...v0.88.1
[0.88.2]: https://github.com/irasikhin/sandboxer/compare/v0.88.1...v0.88.2
[0.88.3]: https://github.com/irasikhin/sandboxer/compare/v0.88.2...v0.88.3
[0.88.4]: https://github.com/irasikhin/sandboxer/compare/v0.88.3...v0.88.4
[0.89.0]: https://github.com/irasikhin/sandboxer/compare/v0.88.4...v0.89.0
[0.90.0]: https://github.com/irasikhin/sandboxer/compare/v0.89.0...v0.90.0
[0.91.0]: https://github.com/irasikhin/sandboxer/compare/v0.90.0...v0.91.0
[0.92.0]: https://github.com/irasikhin/sandboxer/compare/v0.91.0...v0.92.0
[0.93.0]: https://github.com/irasikhin/sandboxer/compare/v0.92.0...v0.93.0
[0.93.1]: https://github.com/irasikhin/sandboxer/compare/v0.93.0...v0.93.1
