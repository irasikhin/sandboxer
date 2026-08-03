#!/usr/bin/env bash
# Assert that named tests actually RAN and passed, from a `go test -v` log.
#
# A skip is not a pass. Every real-engine gate in this repo skips when its
# prerequisite is missing — that is what keeps the suite runnable on a laptop
# without KVM — but the same property makes a CI job that skips EVERYTHING
# indistinguishable from one that verified everything: both are green. That is
# not hypothetical here; it is how a host-auth test stayed broken for ~8
# releases, and it is why the nightly microVM job looked healthy while its
# canary had never once succeeded.
#
# So: in CI, name the tests that MUST have run. A skip becomes a failure with
# the runner's own reason printed, instead of a green tick over nothing.
#
# Usage:
#   scripts/assert-tests-ran.sh <go-test-v.log> <TestName> [TestName ...]
set -euo pipefail

if [[ $# -lt 2 ]]; then
  echo "usage: $0 <go-test-v.log> <TestName> [TestName ...]" >&2
  exit 2
fi

log="$1"
shift

if [[ ! -f "$log" ]]; then
  echo "assert-tests-ran: no log at $log" >&2
  exit 1
fi

missing=()
skipped=()
for name in "$@"; do
  if grep -qE "^--- PASS: ${name}([[:space:]]|\$)" "$log"; then
    continue
  fi
  if grep -qE "^--- SKIP: ${name}([[:space:]]|\$)" "$log"; then
    skipped+=("$name")
  else
    missing+=("$name")
  fi
done

if [[ ${#skipped[@]} -eq 0 && ${#missing[@]} -eq 0 ]]; then
  echo "assert-tests-ran: all $# required test(s) ran and passed."
  exit 0
fi

echo "assert-tests-ran: the e2e job did not actually verify what it claims." >&2
if [[ ${#skipped[@]} -gt 0 ]]; then
  echo >&2
  echo "SKIPPED (prerequisite missing on this runner):" >&2
  for name in "${skipped[@]}"; do
    echo "  - $name" >&2
    # The reason go test prints under the SKIP marker, indented.
    grep -A2 -E "^=== RUN[[:space:]]+${name}\$" "$log" | grep -E "\.go:[0-9]+:" | head -2 | sed 's/^/      /' >&2 || true
  done
fi
if [[ ${#missing[@]} -gt 0 ]]; then
  echo >&2
  echo "NEVER RAN (not in the log at all — renamed, filtered out by -run, or the binary died):" >&2
  for name in "${missing[@]}"; do echo "  - $name" >&2; done
fi
echo >&2
echo "Fix the prerequisite or drop the test from the required list — do not let it skip silently." >&2
exit 1
