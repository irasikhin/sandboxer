#!/usr/bin/env bash
# dsh launcher for the sandboxer toolbox image.
#
# Two jobs, both of which have to happen in argv:
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
# Everything else is passed through untouched.
set -euo pipefail

args=("$@")

if [[ -n "${SANDBOXER_IN_CONTAINER-}" ]]; then
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

exec @node@ --expose-internals @bin@ "${args[@]}"
