#!/usr/bin/env bash
# Opt-in real end-to-end integration suite (needs podman/docker, or smolvm+KVM).
#
# Drives a real container engine (or a real smolvm microVM), real networks and
# the real egress boundary; the coding agent is stubbed. Tests skip cleanly when
# a prerequisite (engine, KVM/HVF, smolvm, pre-pulled smoke image, toolbox image)
# is missing. NOT run in CI, and excluded from the coverage gate by the
# `integration` build tag.
#
# Usage:
#   scripts/itest.sh                      # whole suite (./...)
#   scripts/itest.sh ./internal/egress/   # one package
#   scripts/itest.sh -run TestVM_ ./internal/backend/   # the microVM e2e tests
#
# Env:
#   SANDBOXER_ITEST_ENGINE=docker|podman  # pin the container engine
#   SANDBOXER_ITEST_BUILD_IMAGE=1         # build the toolbox image if absent
#   SANDBOXER_SMOLVM=/path/to/smolvm      # microVM backend binary (else PATH)
#   SANDBOXER_ITEST_VM_IMAGE=<tar|ref>    # image the microVM tests boot (else "alpine")
#
# Run inside `nix develop` (go is not on PATH otherwise).
set -euo pipefail
exec go test -tags integration -count=1 "${@:-./...}"
