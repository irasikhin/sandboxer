# Карта рынка: sandbox для разработки с Claude

Дата: 2026-05-29. Цель: понять, что уже существует, прежде чем писать своё.
Фокус решения: **локально, параллельные агенты, доверенный код, namespace-изоляция.**

## 1. Официально от Anthropic

- **Встроенный `/sandbox` в Claude Code** — OS-level изоляция: bubblewrap (Linux) /
  Seatbelt `sandbox-exec` (macOS) + сетевой прокси-allowlist. По данным Anthropic снижает
  промпты на разрешения на ~84%. Границы: запись в FS ограничена рабочим каталогом, сеть —
  через прокси-allowlist.
  Docs: https://code.claude.com/docs/en/sandboxing
  Инженерный пост: https://www.anthropic.com/engineering/claude-code-sandboxing
- **sandbox-runtime («srt»)** — открытый standalone-инструмент: применяет ограничения FS/сети
  к произвольному процессу без контейнера. На Linux — bubblewrap + сетевой прокси
  (Unix-сокеты, socat-мост, удаление network namespace из bwrap-контейнера → весь трафик
  идёт через прокси).
  Repo: https://github.com/anthropic-experimental/sandbox-runtime
  npm: https://www.npmjs.com/package/@anthropic-ai/sandbox-runtime
  Заметка про обход и патч (контекст границ безопасности):
  https://www.securityweek.com/anthropic-silently-patches-claude-code-sandbox-bypass/

## 2. Docker-обёртки под Claude Code (community / vendor)

- **streamingfast/sbox** — Docker-обёртка для Claude Code (и OpenCode). Бэкенды: Docker
  sandbox (microVM) или обычные контейнеры; per-worktree. https://github.com/streamingfast/sbox
- **arezi/claude-sandbox** — лёгкая обёртка, plain Docker (не Docker Desktop), для Linux.
  https://github.com/arezi/claude-sandbox
- **textcortex/claude-code-sandbox** — запуск без подтверждения каждого разрешения (архив,
  продолжение в Spritz). https://github.com/textcortex/claude-code-sandbox
- **Docker official sandbox** — `docker/sandbox-templates:claude-code`.
  https://docs.docker.com/ai/sandboxes/agents/claude-code/
- **Cloudflare Sandbox SDK** — запуск Claude Code в облачной песочнице (вне локального scope).
  https://developers.cloudflare.com/sandbox/tutorials/claude-code/

## 3. Параллельные агенты

- **Нативные git worktrees** в Claude Code: субагенты могут работать каждый в своём worktree
  (`isolation: worktree` во frontmatter), правки не конфликтуют.
  https://code.claude.com/docs/en/worktrees
- **frankbria/parallel-cc** — автоматизация параллельных версий Claude Code через worktrees +
  координация агентов. https://github.com/frankbria/parallel-cc
- **Neon DB branching** — на каждого субагента своя ветка БД (если нужна изоляция данных).
  https://neon.com/guides/isolated-subagents-neon-branching

## 4. Linux-примитивы (путь «написать своё»)

- **bubblewrap (bwrap)** — unprivileged namespace-песочница; бэкенд Flatpak и srt. Есть на хосте.
- **nsjail** — namespace+seccomp+cgroups+rlimits jail от Google; быстрый старт (~20мс), без демона.
  https://www.morphllm.com/nsjail-sandbox  (на хосте НЕ установлен)
- **firejail** — SUID namespace+seccomp+AppArmor (на хосте НЕ установлен).
- **gVisor (runsc)** — user-space ядро; сильная изоляция, но это уже почти microVM-класс.
- **procjail** — выбирает лучший доступный механизм (bwrap > firejail > unshare > rlimits),
  таймауты, чистка секретов. https://github.com/santhsecurity/procjail
- Обзорный список агентских песочниц 2026-05:
  https://gist.github.com/wincent/2752d8d97727577050c043e4ff9e386e

## 5. Отброшено (вне scope)

| Решение | Почему вне scope |
|---|---|
| **E2B** (Firecracker microVM, ~150мс, SOC2) | облако + microVM; для недоверенного кода |
| **Daytona** (Docker, ~90мс, OSS, Computer Use) | облако/платформа, мультиарендность |
| **Modal / Northflank / Beam / Spheron** | облачные платформы исполнения кода |
| **microsandbox** (libkrun microVM, MCP) | microVM — избыточно для доверенного кода |

Источники сравнений: https://rywalker.com/research/ai-agent-sandboxes ,
https://www.zenml.io/blog/e2b-vs-daytona ,
https://northflank.com/blog/daytona-vs-e2b-ai-code-execution-sandboxes ,
https://github.com/restyler/awesome-sandbox

## Вывод

Для «локально + параллельные агенты + доверенный код» естественный спектр:
**нативный `/sandbox` + worktrees** (минимум) → **srt/Docker-обёртка** (если нативного мало) →
**тонкий свой оркестратор поверх bubblewrap/worktrees** (если эргономики параллели не хватает).
microVM и облако не нужны. Решение — после PoC (см. `../eval/matrix.md`).
