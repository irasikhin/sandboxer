#!/usr/bin/env bash
# PoC 2 — srt (@anthropic-ai/sandbox-runtime): обёртка ПРОИЗВОЛЬНОГО процесса
# (bubblewrap + proxy) с ограничениями FS/сети. Отличие от native: изолирует весь
# процесс целиком, включая сам claude и не-claude инструменты (MCP-серверы и т.п.).
#
# Детерминированные пробы (без LLM) + попытка обернуть весь claude.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$ROOT/poc/.work/srt"; mkdir -p "$WORK"; cd "$WORK"
SRT="$WORK/node_modules/.bin/srt"
denoise(){ grep -ivE "warning: ignoring|trusted-settings|numtide|flake|extra-|Using saved"; }

if [ ! -x "$SRT" ]; then
  echo "== install @anthropic-ai/sandbox-runtime =="
  npm install --silent --no-fund --no-audit @anthropic-ai/sandbox-runtime
fi
echo "srt version: $("$SRT" --version 2>/dev/null | denoise)"

cat > srt-fs-net.json <<'JSON'
{
  "filesystem": { "denyRead": [], "allowWrite": ["."], "denyWrite": [] },
  "network": { "allowedDomains": ["anthropic.com"], "deniedDomains": [] }
}
JSON

echo "== детерминированные пробы (PASS = ограничение сработало) =="
rm -rf box && mkdir box && cd box
probe(){ echo "--- $1"; timeout 60 "$SRT" -s ../srt-fs-net.json -c "$2" 2>&1 | denoise | head -4; }
probe "write inside cwd -> OK"        'echo hi > inside.txt && echo WROTE_INSIDE'
probe "write \$HOME -> read-only"      'echo hi > $HOME/SRT_OUT_$$ 2>&1 || echo BLOCKED'
probe "exec node из /nix/store"        'node --version'        # NixOS: ro-bind всего FS
probe "net anthropic.com -> allow"     'curl -sS -m12 -o/dev/null -w "%{http_code}\n" https://anthropic.com'
probe "net example.com -> block"       'curl -sS -m12 -o/dev/null -w "%{http_code}\n" https://example.com || echo BLOCKED'
[ -f "$HOME/SRT_OUT_$$" ] && { echo "FAIL: запретный файл создан"; rm -f "$HOME/SRT_OUT_$$"; } || echo "OK: запретного файла на хосте нет"
cd ..

echo "== попытка обернуть ВЕСЬ процесс claude (whole-process sandbox) =="
echo "ВАЖНО (NixOS, этот хост): не работает из коробки — srt поднимает свой network-proxy"
echo "через системный D-Bus/tinyproxy, получает 'Failed to connect to system scope bus:"
echo "Operation not permitted'. FS-изоляция при этом исправна; затык в сетевом слое."
cat > srt-claude.json <<'JSON'
{
  "filesystem": { "denyRead": [], "allowWrite": [".", "~/.claude", "~/.cache", "/tmp"], "denyWrite": [] },
  "network": { "allowedDomains": ["api.anthropic.com", "anthropic.com", "statsig.anthropic.com"], "deniedDomains": [] }
}
JSON
rm -rf cbox && mkdir cbox && cd cbox
timeout 120 "$SRT" -s ../srt-claude.json -c 'claude -p "Reply with exactly: PONG-SRT" --model haiku --output-format json' 1>out.txt 2>err.txt
echo "srt-claude exit=$?  (ожидаемо !=0 на этом хосте)"
grep -iE "system scope bus|tinyproxy|permitted" err.txt | denoise | sort -u | head -3
echo "== srt PoC готов. Логи в $WORK/box, $WORK/cbox =="
