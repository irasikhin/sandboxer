# sandboxer task runner — wraps the commands documented in CONTRIBUTING.md
# and the scripts in scripts/. `just` ships in the devShell (flake.nix); run
# `just` (or `just --list`) to list the recipes.

set shell := ["bash", "-euo", "pipefail", "-c"]

# List the available recipes.
default:
    @just --list

# Format the Go sources (gofmt + goimports, per .golangci.yml).
fmt:
    golangci-lint fmt

# Check formatting without rewriting files (non-zero when a file is unformatted).
fmt-check:
    golangci-lint fmt --diff

# Static analysis (go vet).
vet:
    go vet ./...

# Full lint suite (config in .golangci.yml).
lint:
    golangci-lint run ./...

# Run the unit tests; pass go test arguments after the recipe name.
test *ARGS="./...":
    go test {{ARGS}}

# The green gate before a commit: format + vet + lint + tests.
check: fmt-check vet lint test

# Coverage run (race-enabled) plus the 90% total gate — threshold mirrors ci.yml, keep them in step.
cover:
    #!/usr/bin/env bash
    set -euo pipefail
    go test ./... -race -coverprofile=coverage.out
    total=$(go tool cover -func=coverage.out | awk '/^total:/ {sub(/%/,"",$3); print $3}')
    echo "total coverage: ${total}%"
    awk -v t="$total" 'BEGIN { exit (t+0 >= 90.0) ? 0 : 1 }' \
      || { echo "ERROR: total coverage ${total}% is below the 90% gate" >&2; exit 1; }

# Real-integration suite (needs msb + KVM/HVF; passes arguments to scripts/itest.sh).
itest *ARGS="":
    scripts/itest.sh {{ARGS}}

# Build the local binary.
build:
    go build ./cmd/sandboxer

# Flake evaluation checks (what ci.yml's nix job runs before the build).
nix-check:
    nix flake check --no-build --print-build-logs
    nix-instantiate --parse internal/toolbox/assets/flake.nix > /dev/null

# Build the Nix package.
nix-build:
    nix build .#sandboxer --no-link --print-build-logs

# Build the toolbox image into the microVM store (host nix).
image:
    nix run .#build-image

# Cut a release (see CONTRIBUTING.md "Releasing"); passes arguments to scripts/release.sh.
release *ARGS="":
    scripts/release.sh {{ARGS}}
