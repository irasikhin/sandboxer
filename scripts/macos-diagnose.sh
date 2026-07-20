#!/usr/bin/env bash
# sandboxer macOS diagnostic — run this ON A MAC with Docker Desktop (or `podman
# machine start`) running, from a clone of the sandboxer repo on main. It builds
# the binary, exercises the container path, and reports exactly where the
# Linux-first uid / bind-mount / egress assumptions break. Paste the whole
# output back.
#
#   bash macos-diagnose.sh 2>&1 | tee macos-report.txt
set -uo pipefail

say() { printf '\n=== %s ===\n' "$1"; }

say "host facts"
echo "uname=$(uname -s) arch=$(uname -m)"
echo "host uid:gid = $(id -u):$(id -g)"
command -v docker >/dev/null && echo "docker: $(docker --version)"
command -v podman >/dev/null && echo "podman: $(podman --version)"
ENGINE=${SANDBOXER_ENGINE:-$(command -v docker >/dev/null && echo docker || echo podman)}
echo "engine under test = $ENGINE"

say "build sandboxer (darwin)"
if command -v nix >/dev/null; then
  nix develop -c go build -o /tmp/sbx ./cmd/sandboxer || { echo "BUILD FAILED"; exit 1; }
else
  go build -o /tmp/sbx ./cmd/sandboxer || { echo "BUILD FAILED (install go 1.24+)"; exit 1; }
fi
SBX=/tmp/sbx
echo "built: $($SBX --version 2>/dev/null || echo ok)"

say "raw engine probe — the crux: does --user <hostuid> + a bind mount let the container WRITE?"
# This replicates sandboxer's core mount contract without sandboxer, to isolate
# whether the Docker Desktop / podman-machine VM honours the host uid on a bind.
PROBE=$(mktemp -d)
echo hostfile > "$PROBE/f"
$ENGINE run --rm --user "$(id -u):$(id -g)" \
  -v "$PROBE:$PROBE:rw" -w "$PROBE" alpine:latest sh -c '
    echo "in-container uid=$(id -u) gid=$(id -g)"
    if echo written-by-container > probe_out 2>err; then
      echo "BIND WRITE: OK"
    else echo "BIND WRITE: FAILED ->"; cat err; fi
    ls -ln . 2>/dev/null | head' 2>&1
echo "host sees probe_out: $([ -f "$PROBE/probe_out" ] && echo yes || echo NO)"
rm -rf "$PROBE"

say "real sandboxer flow — create + exec in a throwaway git repo"
T=$(mktemp -d); cd "$T"
git init -q; git config user.email you@local; git config user.name you
echo hello > README.md; git add README.md; git commit -qm init
SANDBOXER_NO_EGRESS=1 "$SBX" create smoke --src "$T" 2>&1 | sed 's/^/[create] /'
echo "--- exec: identity, write, commit (the whole value prop) ---"
SANDBOXER_NO_EGRESS=1 "$SBX" exec smoke --src "$T" -- sh -c '
  echo "uid=$(id -u)  pwd=$(pwd)"
  echo more >> README.md
  git add README.md && git commit -m "from sandbox" && echo COMMIT_OK || echo COMMIT_FAIL
' 2>&1 | sed 's/^/[exec] /'
echo "--- host sees the sandbox branch commit? ---"
git -C "$T" log --oneline --all | grep -i "from sandbox" && echo "BRANCH_BACK_OK" || echo "BRANCH_BACK_MISSING"

say "egress smoke (needs the proxy image; skips cleanly if absent)"
"$SBX" exec smoke --src "$T" --allow-domains example.com -- sh -c 'echo egress-path-built' 2>&1 | sed 's/^/[egress] /' | head -8

say "cleanup"
"$SBX" clean "$T" --force 2>&1 | sed 's/^/[clean] /' | head -3
cd /; rm -rf "$T"
echo
echo "DONE. Key questions answered above: (1) does a bind mount honour --user on your"
echo "engine (BIND WRITE), (2) does exec run as the right uid, (3) does commit+identity"
echo "work, (4) does the branch come back. Paste the full output."
