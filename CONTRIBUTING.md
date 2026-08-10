# Contributing to sandboxer

Thanks for taking the time. This project follows a small set of conventions to
make releases reliable; please skim before opening a PR.

> **Conventions live as skills.** The engineering rules below are formalized as vendored **praxis** skills —
> see [`CLAUDE.md`](CLAUDE.md) and `.claude/skills/` (gitignored; `praxis sync` materializes them from the
> committed `praxis.toml`). praxis is internal author-tooling: it is **optional — not required to contribute**.
> The conventions it documents are also enforced by `.golangci.yml`, by CI, and described in [`CLAUDE.md`](CLAUDE.md),
> so you can work from those alone. Where the skills exist they are canonical; this guide mirrors the essentials
> for human contributors browsing the repo, and agents should read the skills rather than duplicate them.

## Local setup

```bash
nix develop                     # Go toolchain

# One-time: install pre-commit hooks (format, lint, test, commit-msg)
pip install pre-commit          # or: nix profile install nixpkgs#pre-commit
pre-commit install
pre-commit install --hook-type commit-msg

# Manual runs
go test ./...                   # unit tests (integration suite: -tags integration)
go vet ./...                    # static analysis
golangci-lint run ./...         # full lint suite (config in .golangci.yml)
nix flake check                 # Nix flake evaluation + checks
nix build .#sandboxer --no-link # Nix package build
pre-commit run --all-files      # all hooks at once
go build ./cmd/sandboxer        # local binary
```

### Integration tests

The default `go test ./...` runs only the in-process unit tests. A separate
real end-to-end suite, behind the `integration` build tag, drives a **real**
microsandbox (`msb`) on KVM (HVF on macOS), real machines and the real
network-policy egress boundary — only the coding agent is stubbed (the
proprietary `claude`/`codex` binaries are never invoked). Prerequisites: `msb`
on `PATH` (or `SANDBOXER_MSB`), `/dev/kvm` on Linux, and a **short**
`MSB_HOME` (e.g. `/tmp/msb` — every sandbox's agent socket derives from it and
must fit sun_path). Run it explicitly, inside `nix develop` (which puts `msb`
on `PATH`):

```bash
scripts/itest.sh                                   # whole suite (./...)
scripts/itest.sh -run TestMSB_ ./internal/backend/ # the msb e2e tests
go test -tags integration -count=1 ./...           # equivalent to the script

# Boot a specific image instead of the default public `alpine` ref:
SANDBOXER_ITEST_MSB_IMAGE=sandboxer-toolbox:latest scripts/itest.sh ./internal/backend/
```

Each test skips cleanly when its prerequisite is missing — no `msb`, no
`/dev/kvm` — so a partial environment never fails the run; read the skips, not
just the exit status.

These tests are excluded from the coverage measurement of the **GitHub
Actions** CI 90% gate (the tag excludes them from the normal build, so they
contribute neither lines nor coverage) — that gate stays hypervisor-free.
The msb slice runs in CI on KVM-capable runners (`ci.yml`'s `itest` job), and
the **homelab Jenkins** e2e job (`Jenkinsfile`) builds the real toolbox image
with nix, loads it into msb's store and runs the whole suite. The mapping of
every exported function to its unit/integration coverage lives in
[`docs/test-coverage-audit.md`](docs/test-coverage-audit.md).

## Code quality tools

| Tool | Purpose | Config |
|------|---------|--------|
| `gofmt` / `goimports` | Formatting + import ordering | — |
| `go vet` | Built-in static analysis | — |
| `golangci-lint` | Mega-linter (50+ checks) | `.golangci.yml` |
| `pre-commit` | Git hooks framework | `.pre-commit-config.yaml` |
| `commit-msg` hook | Conventional Commits validator | `scripts/git-hooks/commit-msg` |

All checks run in CI on every push and PR — let the hooks catch issues early.

## Conventional Commits

Every commit on `main` and every commit in a PR **must** follow the
[Conventional Commits 1.0.0](https://www.conventionalcommits.org/) spec. The
release script derives the changelog and the next semver from these messages,
so consistency matters.

Format:

```
<type>(<optional scope>)<!>: <subject>

<optional body>

<optional footer(s)>
```

Allowed types:

| Type       | Use it for                                                 | Triggers bump |
|------------|------------------------------------------------------------|---------------|
| `feat`     | New user-visible feature                                   | minor         |
| `fix`      | Bug fix                                                    | patch         |
| `perf`     | Performance change without behaviour change                | patch         |
| `refactor` | Code restructuring with no functional change               | —             |
| `docs`     | Documentation only                                         | —             |
| `test`     | Adding or revising tests                                   | —             |
| `build`    | Build system, packaging (`flake.nix`, `nix/`, `go.mod`)    | —             |
| `ci`       | CI pipelines (`.github/workflows/...`)                     | —             |
| `chore`    | Routine maintenance (deps bump, file moves)                | —             |
| `revert`   | Reverting a previous commit                                | —             |
| `style`    | Whitespace / formatting only                               | —             |

A `!` after the type/scope **or** a `BREAKING CHANGE:` footer means a major
bump. Use it whenever a flag, command, output format, or config key changes
shape in an incompatible way.

### Examples

```
feat(cli): add --backend flag to exec

fix: release the egress proxy port on teardown

refactor: extract the profile loader from config.go

feat(config)!: drop the .nix profile format

BREAKING CHANGE: profiles must now be YAML; .nix profiles are no longer read.
```

Subject line rules:

* imperative mood ("add X", not "added X")
* lowercase first letter
* no trailing period
* keep under ~70 characters

## Branch / PR workflow

1. Branch off `main`.
2. Group related changes into focused commits — `git rebase -i` to squash
   work-in-progress commits before opening the PR.
3. Ensure `go test ./...` and `go vet ./...` are clean.
4. Open the PR; CI runs the matrix build + tests.
5. Maintainers merge; do not self-merge unless explicitly invited.

## Releasing

Releases are tag-driven and version is derived from
[Conventional Commits](https://www.conventionalcommits.org/) since the
previous stable tag. After merging the bumping commit(s), the maintainer
runs:

```bash
./scripts/release.sh                # auto-detect bump from commit history
./scripts/release.sh auto           # same as above, explicit
./scripts/release.sh patch          # manual override
./scripts/release.sh X.Y.Z          # explicit version (e.g. for transitions)
git push --follow-tags
```

Bump rule (highest impact wins):

| Commit                                   | Bump in 0.x | Bump in 1.x+ |
|------------------------------------------|-------------|--------------|
| `BREAKING CHANGE:` footer, or `type!:`   | minor       | major        |
| `feat:`                                  | minor       | minor        |
| `fix:`                                   | patch       | patch        |
| Anything else (refactor, ci, docs, ...)  | patch       | patch        |

In 0.x, semver permits anything to break; the convention is to bump
minor for breaking changes (`0.2.x` → `0.3.0`). Crossing `0.x` → `1.x`
is an explicit decision — pass `1.0.0` as the version when ready.

The CHANGELOG section covers everything since the last tag (including
prereleases), but `auto`/`patch`/`minor`/`major` refuse to bump from a
prerelease base — pass an explicit `X.Y.Z` for the transition out of an
alpha/beta/rc series.

The release script:

* computes the next semver from commits since the last stable tag,
* updates `version` in `nix/package.nix` to match,
* writes a new section to `CHANGELOG.md` grouping commits by type,
* commits with `chore(release): vX.Y.Z`, then tags `vX.Y.Z`.

Pushing the tag triggers the GitHub Actions release workflow
([`.github/workflows/release.yml`](.github/workflows/release.yml)): it verifies
the tag matches `nix/package.nix`, builds the Linux binaries (amd64 + arm64) and
publishes a GitHub release with the changelog section. It uses the
auto-provisioned `GITHUB_TOKEN` — no extra secret to configure. Branch/PR CI
lives in [`.github/workflows/ci.yml`](.github/workflows/ci.yml).

Before pushing the release tag, verify:

* `git status --short` is clean.
* `go test ./...`, `go vet ./...`, `golangci-lint run ./...`,
  `nix flake check`, and `nix build .#sandboxer --no-link` pass.
* `CHANGELOG.md` has the intended section and `nix/package.nix` matches the tag.
* After publishing, smoke-test `nix profile install github:irasikhin/sandboxer`.

## Style

Go style and formatting are owned by the `lang-go-style` and `build-go-tooling` skills (see
[`CLAUDE.md`](CLAUDE.md)): `gofmt`/`goimports` with stdlib / third-party / local
(`github.com/irasikhin/sandboxer`) import groups, small narrow types, accept-interfaces / return-structs.

Repo-specific: new flags are wired into the cobra command structs and documented via their struct tags — the
help text is generated from them.

## Config resolution

There is **no config-level inheritance**: no defaults block, no global config, no merging between files — a
profile is exactly what its file/section says (reuse inside one file is ordinary nix — let bindings and
`//` merges, resolved by the evaluator). The user-facing precedence chain (`flags > profile > SANDBOXER_*
env > built-in`) is documented in the README. How it maps to code:

- The **project config** is `sandboxer.nix` (`config.ConfigPath()`), discovered in the cwd and evaluated
  via the host nix (`config.EvalConfig`: `nix-instantiate --eval --strict --json` under restrict-eval).
- **`Document.Select`** (`internal/config/document.go`) picks a self-contained `profiles:` section (or the
  flat file's single profile). Profiles live in ONE config file — no directory scan, no global store.
- Scalar precedence lives in `config.ResolveRuntime` (`internal/config/runtime.go`).
