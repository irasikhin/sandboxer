# Contributing to sandboxer

Thanks for taking the time. This project follows a small set of conventions to
make releases reliable; please skim before opening a PR.

## Local setup

```bash
nix develop                     # Go toolchain

# One-time: install pre-commit hooks (format, lint, test, commit-msg)
pip install pre-commit          # or: nix profile install nixpkgs#pre-commit
pre-commit install
pre-commit install --hook-type commit-msg

# Manual runs
go test ./...                   # unit + integration tests
go vet ./...                    # static analysis
golangci-lint run ./...         # full lint suite (config in .golangci.yml)
nix flake check                 # Nix flake evaluation + checks
nix build .#sandboxer --no-link # Nix package build
pre-commit run --all-files      # all hooks at once
go build ./cmd/sandboxer        # local binary
```

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
| `ci`       | CI workflows (`.github/workflows/...`)                      | —             |
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

The `release` workflow in `.github/workflows/release.yml` builds the Linux
binaries (amd64 + arm64) for each tag and publishes a GitHub Release with the
changelog section.

Before pushing the release tag, verify:

* `git status --short` is clean.
* `go test ./...`, `go vet ./...`, `golangci-lint run ./...`,
  `nix flake check`, and `nix build .#sandboxer --no-link` pass.
* `CHANGELOG.md` has the intended section and `nix/package.nix` matches the tag.
* After publishing, smoke-test `nix profile install github:irasikhin/sandboxer`.

## Style

* Run `gofmt` (your editor probably does). Keep imports in the standard
  stdlib / third-party / local (`github.com/irasikhin/sandboxer`) groups.
* Prefer small, narrow types over large structs that grow over time.
* New flags must be wired into the CLI command structs and documented via
  their struct tags — the help text is generated from them.
