#!/usr/bin/env bash
# Opt-in real end-to-end integration suite (needs podman/docker, or smolvm+KVM).
#
# Drives a real container engine (or a real microVM runner), real networks and
# the real egress boundary; the coding agent is stubbed. Tests skip cleanly when
# a prerequisite (engine, KVM/HVF, smolvm/msb, pre-pulled smoke image, toolbox
# image) is missing — so read the skips, not just the exit status. Excluded from
# the coverage gate by the `integration` build tag.
#
# CI runs SLICES of this suite, never the whole thing: ci.yml runs the
# engine-level tests on docker, e2e.yml runs the microVM legs nightly on KVM
# runners, and the Jenkins job runs everything. The container `podman` leg and
# the toolbox-image-gated tests are local-only — run them here before a release.
#
# Usage:
#   scripts/itest.sh                      # whole suite (./...)
#   scripts/itest.sh ./internal/egress/   # one package
#   scripts/itest.sh -run TestVM_ ./internal/backend/    # the smolvm e2e tests
#   scripts/itest.sh -run TestMSB_ ./internal/backend/   # the microsandbox e2e tests
#
# Env:
#   SANDBOXER_ITEST_ENGINE=docker|podman  # pin the container engine
#   SANDBOXER_ITEST_BUILD_IMAGE=1         # build the toolbox image if absent
#   SANDBOXER_ITEST_SKIP_LIVE_EGRESS=1    # skip checks needing real outbound
#   SANDBOXER_SMOLVM=/path/to/smolvm      # smolvm binary (else PATH)
#   SANDBOXER_ITEST_VM_IMAGE=<tar|ref>    # image the smolvm tests boot (else "alpine")
#   SANDBOXER_MSB=/path/to/msb            # microsandbox binary (else PATH)
#   SANDBOXER_ITEST_MSB_IMAGE=<ref>       # image the msb tests boot (else "alpine")
#   MSB_HOME=/tmp/msb                     # keep SHORT: every sandbox's agent
#                                         # socket derives from it (sun_path 108)
#
# podman needs its own config to exist before any of this works — a policy.json
# and a registries.conf (~/.config/containers/ or /etc/containers/). The devShell
# ships the podman BINARY only, so on a host without a system-wide podman install
# the podman leg fails on "no policy.json file found"; see docs/troubleshooting.md.
#
# Run inside `nix develop` (go is not on PATH otherwise).
set -euo pipefail
exec go test -tags integration -count=1 "${@:-./...}"
