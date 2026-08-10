#!/usr/bin/env bash
# Opt-in real end-to-end integration suite (needs msb + KVM, or HVF on macOS).
#
# Drives a real microsandbox runner: real machines, real virtio-fs shares and
# the real network-policy egress boundary; the coding agent is stubbed. Tests
# skip cleanly when a prerequisite (msb, /dev/kvm, bootable image) is missing —
# so read the skips, not just the exit status. Excluded from the coverage gate
# by the `integration` build tag.
#
# CI runs SLICES of this suite: ci.yml runs the msb slice on KVM-capable
# runners, and the Jenkins job builds the real toolbox image and runs
# everything. The toolbox-image-gated tests are otherwise local — run them here
# before a release.
#
# Usage:
#   scripts/itest.sh                                      # whole suite (./...)
#   scripts/itest.sh ./internal/backend/                  # one package
#   scripts/itest.sh -run TestMSB_ ./internal/backend/    # the msb e2e tests
#
# Env:
#   SANDBOXER_MSB=/path/to/msb            # microsandbox binary (else PATH)
#   SANDBOXER_ITEST_MSB_IMAGE=<ref>       # image the msb tests boot (else "alpine")
#   MSB_HOME=/tmp/msb                     # keep SHORT: every sandbox's agent
#                                         # socket derives from it (sun_path 108)
#
# Run inside `nix develop` (go and msb are not on PATH otherwise).
set -euo pipefail
exec go test -tags integration -count=1 "${@:-./...}"
