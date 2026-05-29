#!/usr/bin/env bash
# PoC 1 — Native: git worktrees + нативный Claude Code /sandbox + headless autonomous.
# Доказывает: per-agent изоляция FS (bwrap), автономность (bypassPermissions),
# параллель по worktree, merge. + негативный тест выхода за пределы cwd.
#
# Стоимость: использует модель haiku (дёшево). Меняется через MODEL=... .
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEMPLATE="$ROOT/poc/sample-repo"
WORK="$ROOT/poc/.work/native"
LOGS="$WORK/logs"
MODEL="${MODEL:-haiku}"
REPO="$WORK/repo"

rm -rf "$WORK"; mkdir -p "$LOGS"

echo "== [native] подготовка стенд-репо =="
cp -r "$TEMPLATE" "$REPO"
cd "$REPO"
git init -q && git add -A && git -c user.email=poc@local -c user.name=poc commit -q -m "init sample-repo"

# Включаем нативный sandbox для всех агентов этого репо.
# allowedDomains оставляем для bash-команд (нашим задачам сеть не нужна; claude-процесс
# работает вне песочницы и достукивается до API сам).
mkdir -p .claude
cat > .claude/settings.json <<'JSON'
{
  "sandbox": {
    "enabled": true,
    "autoAllowBashIfSandboxed": true,
    "network": { "allowedDomains": ["api.anthropic.com", "registry.npmjs.org"] }
  }
}
JSON
git add -A && git -c user.email=poc@local -c user.name=poc commit -q -m "enable native sandbox"

# Задачи -> ветки
declare -A TASKS=(
  [multiply]="Добавь в src/calc.js функцию multiply(a,b) и тест в tests/calc.test.js. Запусти 'npm test' и добейся зелёных тестов."
  [docs]="В README.md добавь раздел '## API' с описанием всех экспортируемых функций из src/calc.js. Код не трогай."
  [validate]="Отрефактори add и subtract в src/calc.js: бросать TypeError если аргумент не число. Добавь тест на ошибку в tests/calc.test.js. Запусти 'npm test' и добейся зелёных тестов."
)

run_agent () {
  local name="$1" body="$2"
  local wt="$WORK/wt-$name"
  git worktree add -q -b "task/$name" "$wt" HEAD
  ( cd "$wt"
    local t0=$SECONDS
    claude -p "Ты в git worktree, работай автономно, без вопросов. Не выходи за пределы текущего каталога. Задача: $body" \
      --permission-mode bypassPermissions --model "$MODEL" \
      --output-format json > "$LOGS/$name.json" 2> "$LOGS/$name.err"
    echo "exit=$? secs=$((SECONDS-t0))" > "$LOGS/$name.meta"
  ) &
}

echo "== [native] запуск 3 агентов параллельно (model=$MODEL) =="
for name in "${!TASKS[@]}"; do run_agent "$name" "${TASKS[$name]}"; done
wait
echo "== [native] агенты завершились =="

# Результаты + проверка тестов в каждом worktree
for name in "${!TASKS[@]}"; do
  wt="$WORK/wt-$name"
  echo "--- $name ($(cat "$LOGS/$name.meta" 2>/dev/null)) ---"
  ( cd "$wt" && node --test >/dev/null 2>&1 && echo "  tests: PASS" || echo "  tests: (нет/красные — см. ниже)" )
  ( cd "$wt" && git --no-pager diff --stat HEAD 2>/dev/null | sed 's/^/  diff: /' )
done

# Негативный тест изоляции: просим агента выполнить запись за пределы cwd.
echo "== [native] негативный тест: запись в \$HOME из песочницы должна ПАДАТЬ =="
ESCAPE="$HOME/SANDBOX_ESCAPE_NATIVE_$$"
rm -f "$ESCAPE"
( cd "$REPO" && claude -p "Выполни РОВНО эту bash-команду и сообщи stderr и код возврата, ничего больше: echo hi > $ESCAPE" \
    --permission-mode bypassPermissions --model "$MODEL" --output-format json > "$LOGS/escape.json" 2> "$LOGS/escape.err" )
if [ -f "$ESCAPE" ]; then echo "  РЕЗУЛЬТАТ: файл создан -> sandbox НЕ заблокировал (FAIL изоляции)"; rm -f "$ESCAPE";
else echo "  РЕЗУЛЬТАТ: файла нет -> запись заблокирована (PASS изоляции)"; fi

echo "== [native] merge всех веток в main =="
cd "$REPO"
for name in "${!TASKS[@]}"; do
  git merge --no-edit -q "task/$name" 2>>"$LOGS/merge.err" && echo "  merged task/$name OK" || echo "  merge task/$name КОНФЛИКТ (см. merge.err)"
done
echo "== [native] финальные тесты после merge =="
node --test 2>&1 | tail -5
echo "== [native] готово. Логи: $LOGS =="
