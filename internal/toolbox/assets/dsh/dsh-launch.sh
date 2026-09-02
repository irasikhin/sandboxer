#!/usr/bin/env bash
# dsh launcher for the sandboxer toolbox image.
#
# Three jobs, all of which have to happen in argv:
#
#   1. --expose-internals. The Cordis loader reaches Node's internal module
#      loader either through that flag or through the node-addon-require-builtin
#      addon, and the addon pattern-matches the node binary's own machine code —
#      a nixpkgs-built node is not a shape it recognizes. Without the flag NO
#      profile boots, and NODE_OPTIONS refuses this flag.
#
#   2. The web bind overlay, INSIDE A SANDBOX ONLY. dsh's web UI binds the
#      guest's loopback by default, which a published port never reaches (the
#      forward is delivered to the guest's eth0), and `--host 0.0.0.0` is
#      refused by dsh itself — so plain `dsh web` would always look broken from
#      the host's browser. The overlay changes the row's FALLBACK only, leaving
#      an explicit `--host` to win; see web-bind.patch.yml for why this is the
#      right default in a microVM. `--patch` is a LAUNCHER flag that the `web`
#      alias explicitly rejects, so the alias is rewritten to its documented
#      long form (`--profile web`) when the overlay goes in.
#
#   3. The v8 heap cap, INSIDE A SANDBOX ONLY (see the in-script comment:
#      the machine's memory cap is a hard ceiling, and exceeding it is a
#      diagnostic-free SIGKILL — bounding the heap turns that into a loud
#      heap error instead).
#
# Everything else is passed through untouched.
set -euo pipefail

args=("$@")

# Inside a microVM the machine's memory cap is a HARD ceiling (no swap): a
# run that exceeds it is SIGKILLed by the guest kernel with no diagnostics at
# all — the shell just prints "Killed". Bound v8's heap below the ceiling
# instead, so a spike ends in visible GC pressure, and an overrun is a loud
# heap error rather than a silent kill. Sized from the guest's own MemTotal
# (leave the rest of the machine headroom); machines under 1 GiB are left
# uncapped — a cap that small would cripple the harness the user explicitly
# asked to run there.
heap=()
if [[ -n "${SANDBOXER_IN_CONTAINER-}" ]]; then
  if mem_kib=$(awk '/^MemTotal:/ {print $2}' /proc/meminfo 2>/dev/null) && [[ -n $mem_kib ]]; then
    mib=$(( mem_kib / 1024 - 512 ))
    if (( mib >= 512 )); then
      heap=(--max-old-space-size=$mib)
    fi
  fi

  web=0
  if [[ ${1-} == web ]]; then
    web=1
    shift
    args=(--profile web "$@")
  else
    for ((i = 0; i < ${#args[@]}; i++)); do
      if [[ ${args[i]} == --profile=web ]] ||
        { [[ ${args[i]} == --profile ]] && [[ ${args[i + 1]-} == web ]]; }; then
        web=1
        break
      fi
    done
  fi
  if ((web)) && [[ -r @patch@ ]]; then
    args=(--patch @patch@ "${args[@]}")
  fi
fi

exec @node@ --expose-internals "${heap[@]}" @bin@ "${args[@]}"
