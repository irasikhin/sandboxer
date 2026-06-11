#!/usr/bin/env bash
# Opt-in real end-to-end integration suite (needs podman or docker).
#
# Drives a real container engine, real networks and the real egress proxy; the
# coding agent is stubbed. Tests skip cleanly when a prerequisite (engine,
# pre-pulled smoke image, toolbox image) is missing. NOT run in CI, and excluded
# from the coverage gate by the `integration` build tag.
#
# Usage:
#   scripts/itest.sh                      # whole suite (./...)
#   scripts/itest.sh ./internal/egress/   # one package
#   scripts/itest.sh -run TestProxyInContainer_RealProxyBinary ./internal/egress/
#
# Env:
#   SANDBOXER_ITEST_ENGINE=docker|podman  # pin the engine
#   SANDBOXER_ITEST_BUILD_IMAGE=1         # build the toolbox image if absent
#
# Run inside `nix develop` (go is not on PATH otherwise).
set -euo pipefail
exec go test -tags integration -count=1 "${@:-./...}"
