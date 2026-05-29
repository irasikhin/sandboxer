# sandboxer

Инструмент для запуска **нескольких Claude Code агентов параллельно**, каждый в своей
изоляции (отдельный git worktree + песочница FS/сети), **автономно** (без постоянных
промптов на разрешения), на **локальной Linux-машине**.

Статус: **исследование / PoC** (build-or-adopt ещё не решён — см. decision-log ниже).

## Требования

- Параллельный запуск N агентов, изоляция **на агента**.
- Только локальная Linux-машина (текущий хост).
- Код свой и доверенный → достаточно namespace-песочницы; microVM (Firecracker/libkrun) не нужен.
- Подход: сначала PoC готовых решений → затем решение build-or-adopt по фактам.

## Факты хоста (проверено)

| Компонент | Значение |
|---|---|
| `claude` | 2.1.156 — нативные `/sandbox` и worktrees (`isolation: worktree`) |
| `git` | 2.50.1 |
| `docker` | 27.5.1 |
| `bwrap` (bubblewrap) | 0.11.2 |
| `node` | v22.20.0 |
| unprivileged userns | включены (`max_user_namespaces=110758`) |
| cgroup v2 | присутствует (лимиты ресурсов) |
| `nsjail` / `firejail` | нет (не критично — есть bubblewrap) |
| ОС | **NixOS** (`/nix/store`, `/run/current-system`) |
| `/dev/kvm` | есть (microVM возможен, но вне scope) |

**NixOS-нюанс:** профили bind-mount для bubblewrap/srt отличаются от обычного FHS
(нет привычных `/usr`, `/lib` — всё в `/nix/store`). Docker-образы от этого абстрагированы.
Это ключевой фактор оценки.

## Структура

```
README.md              — этот файл + decision-log
docs/market-research.md — карта рынка, ссылки, что отброшено и почему
eval/matrix.md          — таблица оценки трёх кандидатов (1–5 по критериям)
poc/
  tasks.md             — 3 независимые задачи для агентов (общая нагрузка)
  sample-repo/         — стендовый git-репо (Node, без внешних зависимостей)
  native.sh            — PoC 1: worktrees + нативный /sandbox
  srt.sh               — PoC 2: claude в anthropic sandbox-runtime (bubblewrap)
  docker-sbox.sh       — PoC 3: streamingfast/sbox, контейнер на агента
```

## Кандидаты на PoC

1. **Native** — `git worktree` + `claude` headless в нативном `/sandbox`. Нулевая установка, базовая планка.
2. **srt** — [anthropic-experimental/sandbox-runtime](https://github.com/anthropic-experimental/sandbox-runtime) (bubblewrap) вокруг `claude`.
3. **Docker-per-agent** — [streamingfast/sbox](https://github.com/streamingfast/sbox), контейнер на worktree.

Вне scope: облачные платформы (E2B, Daytona, Modal, Northflank) и microVM (microsandbox) —
не «локально + доверенный код». Подробности в `docs/market-research.md`.

## Decision-log

- **2026-05-29** — Greenfield. Уточнены требования (параллельные агенты, локально, доверенный
  код, namespace-изоляция). Проверен хост. Принят двухшаговый подход: PoC → build-or-adopt.
- **2026-05-29 — РЕШЕНИЕ: adopt native + тонкий оркестратор. Свою песочницу НЕ пишем.**
  Прогнаны 3 PoC (см. `eval/matrix.md`), баллы 54 / 44 / 46:
  - **Native** (`/sandbox` + git worktrees + headless `claude --permission-mode bypassPermissions`)
    закрывает ВСЕ требования без кода песочницы — только `.claude/settings.json`. Доказано:
    3 агента параллельно (15–26с, haiku), изоляция FS подтверждена (запись в `$HOME` → read-only),
    автономность, чистый merge 3 веток, тесты зелёные, **на NixOS из коробки** (весь FS ro →
    `/nix/store` читается). Зависимости (bubblewrap, socat) уже стоят.
  - **srt** — отличная FS-изоляция всего процесса, но обёртка claude с сетью упёрлась в
    systemd/proxy на этом NixOS-хосте. Ниша: изоляция не-claude процессов / всего агента.
  - **Docker-per-agent** — сильнейшая граница, NixOS-agnostic, но требует claude-в-образе +
    авторизацию; тяжелее для доверенного локального кода. Ниша: недоверенный код / воспроизводимость.
  - **Единственный пробел native — оркестрация UX** (N worktree + N агентов + логи/диффы + merge).
    Прототип уже есть: `poc/native.sh`. → Фаза 3: тонкий `sandboxer` поверх native.
- _Следующий шаг: построить MVP тонкого оркестратора (Фаза 3 плана) — по подтверждению пользователя._
