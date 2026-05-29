#!/usr/bin/env bash
# PoC 3 — Docker-per-agent: контейнер на агента/worktree. Изоляция = контейнер
# (namespaces): хостовый FS скрыт целиком, кроме явного bind-mount worktree; сеть
# полностью управляема (--network none / proxy). Паттерн автоматизирует streamingfast/sbox.
#
# Детерминированная проба механизма (без LLM). Реальный claude-в-контейнере НЕ собираем —
# его трение (claude в образе + авторизация + сеть к API) задокументировано как находка.
set -uo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$ROOT/poc/.work/docker"; WT="$WORK/wt"
rm -rf "$WORK"; mkdir -p "$WT"; cp -r "$ROOT/poc/sample-repo/"* "$WT"/

IMG="$(docker images --format '{{.Repository}}:{{.Tag}}' | grep -E '^node:' | head -1)"
IMG="${IMG:-node:22-bookworm}"
echo "image: $IMG"

echo "== старт + node --test в bind-mounted worktree (сеть отрезана) =="
t0=$(date +%s%N)
docker run --rm -v "$WT":/work -w /work --network none "$IMG" sh -c 'node --test 2>&1 | tail -3; node --version'
echo "container_wall_ms=$(( ($(date +%s%N)-t0)/1000000 ))"

echo "== FS-изоляция: хостовый \$HOME не виден (PASS) =="
docker run --rm -v "$WT":/work -w /work "$IMG" sh -c '[ -e /home/ir/.ssh ] && echo "FAIL: виден ~/.ssh" || echo "PASS: хостовый HOME не виден"'

echo "== сеть: --network none режет DNS (PASS) =="
docker run --rm --network none "$IMG" node -e 'require("dns").lookup("anthropic.com",e=>console.log(e?"PASS: net blocked":"FAIL: net open"))'

cat <<'NOTE'

== Находки по docker-per-agent ==
+ Сильнейшая изоляция из трёх: хостовый FS скрыт целиком (кроме mount), сеть полностью под контролем.
+ NixOS-agnostic: userland берётся из образа, проблем с /nix/store нет.
+ worktree на хосте (bind-mount) -> git/diff/merge на хосте, как в native.
+ старт ~1с при закэшированном образе.
- Чтобы гонять РЕАЛЬНЫЙ claude: нужен claude в образе (npm i -g @anthropic-ai/claude-code
  или docker/sandbox-templates:claude-code) + авторизация в контейнере (mount ~/.claude или
  ANTHROPIC_API_KEY) + сеть к api.anthropic.com (для тонкого контроля — proxy-allowlist).
- Накладные расходы: образ на диск, ресурсы на контейнер.
=> Этот паттерн «под ключ» даёт streamingfast/sbox (per-worktree контейнер + claude).
   Оправдан, когда нужна максимально сильная граница/воспроизводимость; для доверенного
   локального кода тяжелее, чем native.
NOTE
echo "== docker PoC готов =="
