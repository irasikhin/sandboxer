#!/usr/bin/env bash
# dsh launcher for the sandboxer toolbox image.
#
# Four jobs, all of which have to happen in argv:
#
#   1. --expose-internals. The Cordis loader reaches Node's internal module
#      loader either through that flag or through the node-addon-require-builtin
#      addon, and the addon pattern-matches the node binary's own machine code —
#      a nixpkgs-built node is not a shape it recognizes. Without the flag NO
#      profile boots, and NODE_OPTIONS refuses this flag.
#
#   2. Baked plugins, INSIDE A SANDBOX ONLY (see the script's own functions).
#      The image ships a curated set of community plugins INSIDE the dsh
#      package tree; a profile boots them once its dsh.profile.bundles names
#      them, so before exec we initialize the profile (when missing, with the
#      same shipped template dsh itself would use) and merge in the baked
#      bundle names. Additive and idempotent; opt out with
#      SANDBOXER_NO_DSH_PLUGINS=1.
#
#   3. The web bind overlay, INSIDE A SANDBOX ONLY. dsh's web UI binds the
#      guest's loopback by default, which a published port never reaches (the
#      forward is delivered to the guest's eth0), and `--host 0.0.0.0` is
#      refused by dsh itself — so plain `dsh web` would always look broken from
#      the host's browser. The overlay changes the row's FALLBACK only, leaving
#      an explicit `--host` to win; see web-bind.patch.yml for why this is the
#      right default in a microVM. `--patch` is a LAUNCHER flag that the `web`
#      alias explicitly rejects, so the alias is rewritten to its documented
#      long form (`--profile web`) when the overlay goes in.
#
#   4. The v8 heap cap, INSIDE A SANDBOX ONLY (see the in-script comment:
#      the machine's memory cap is a hard ceiling, and exceeding it is a
#      diagnostic-free SIGKILL — bounding the heap turns that into a loud
#      heap error instead).
#
# Everything else is passed through untouched.
set -euo pipefail

# ensure_dsh_profile <name> — initialize a dsh profile the image ships baked
# plugins for (web, headless) and merge the baked bundle names into its
# dsh.profile.bundles, so the plugins are present from the very first boot
# and stay present across image bumps. Profiles the image does not bake are
# never touched.
#
# The initialization replicates dsh's own initProfile byte for byte (manifest
# shape, empty user patch layer, pnpm workspace settings), read from the SAME
# dsh release this launcher ships with — the templates live beside dsh, not
# in the wrapper. An existing manifest is user state (possibly seeded from
# the host home): only the missing baked bundle names are appended, nothing
# is reordered or removed, and a manifest jq cannot parse is left alone.
ensure_dsh_profile() {
  local name=$1
  [[ -n $name ]] || return 0
  local home="${DSH_HOME:-$HOME/.dsh}"
  local dir="$home/profiles/$name"

  local spec bundles patchreload
  spec=$(jq -r --arg p "$name" '.[$p] // empty' @profiles@ 2>/dev/null) || return 0
  [[ -n $spec ]] || return 0
  bundles=$(jq -r '.bundles' <<<"$spec")
  patchreload=$(jq -r '.patchReload' <<<"$spec")
  local plugins
  plugins=$(jq -c '.plugins' <<<"$spec")

  mkdir -p "$dir"
  if [[ ! -f "$dir/package.json" ]]; then
    jq -n --arg name "dsh-profile-$name" --argjson bundles "$bundles" \
      --argjson plugins "$plugins" --arg pr "$patchreload" '
        { name: $name, private: true, dependencies: {},
          dsh: { profile: { bundles: ($bundles + $plugins), patchReload: $pr } } }' \
      > "$dir/package.json"
    cat > "$dir/cordis.patch.yml" <<'EOF'
# Your patch layer for this dsh profile, applied after every bundle layer:
# a top-level YAML array of loader patch entries (id-targeted config
# overrides, disables, and insert lists; `!!js` expressions allowed).
[]
EOF
    cat > "$dir/pnpm-workspace.yaml" <<'EOF'
packages:
  - .

nodeLinker: hoisted
autoInstallPeers: false
EOF
  else
    # Merge only into a manifest whose dsh.profile.bundles is an array;
    # rewrite the file only when a name was actually added.
    local merged
    if ! jq -e --argjson plugins "$plugins" '
         (.dsh.profile.bundles | type) == "array"
         and (($plugins - .dsh.profile.bundles) | length) == 0' \
         "$dir/package.json" >/dev/null 2>&1; then
      if merged=$(jq --argjson plugins "$plugins" '
           if (.dsh.profile.bundles | type) != "array" then error("no bundles")
           else . end
           | .dsh.profile.bundles += ($plugins - .dsh.profile.bundles)' \
           "$dir/package.json" 2>/dev/null); then
        printf '%s\n' "$merged" > "$dir/package.json"
      fi
    fi
  fi

  # Baked plugin packages resolve from the dsh installation for bundle
  # loading (dsh resolves installation first, then the profile), but plugin
  # code may require its own package from the profile's baseUrl — archify
  # resolves its Skill root exactly that way. Link each baked plugin into the
  # profile's node_modules where pnpm would have installed it; dsh's own
  # module-fallback healer never removes links it does not own, and a later
  # `dsh plugin add` of the same package simply replaces the link.
  local n scope
  mkdir -p "$dir/node_modules"
  for n in $(jq -r '.plugins[]' <<<"$spec"); do
    case $n in
      @*/*) scope=${n%%/*}; mkdir -p "$dir/node_modules/$scope" ;;
    esac
    ln -sfn "@node_modules@/$n" "$dir/node_modules/$n"
  done
}

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
fi

# Resolve the profile this invocation boots (the `web` alias becomes the
# long form so the overlay flag it rejects is never handed to it). `dsh
# plugin --profile <name>` also names a profile, and gets the same
# initialization — dsh's own plugin path initializes on first use too.
profile=
for ((i = 0; i < ${#args[@]}; i++)); do
  case ${args[i]} in
    --profile=*)
      profile=${args[i]#--profile=}
      ;;
    --profile)
      profile=${args[i + 1]-}
      ;;
  esac
done
if [[ -z $profile && ${1-} == web ]]; then
  shift
  args=(--profile web "$@")
  profile=web
fi
web=0
[[ $profile == web ]] && web=1

if [[ -n "${SANDBOXER_IN_CONTAINER-}" && -z "${SANDBOXER_NO_DSH_PLUGINS-}" ]]; then
  ensure_dsh_profile "$profile"
fi

if [[ -n "${SANDBOXER_IN_CONTAINER-}" ]] && (( web )) && [[ -r @patch@ ]]; then
  args=(--patch @patch@ "${args[@]}")
fi

exec @node@ --expose-internals "${heap[@]}" @bin@ "${args[@]}"
