# sandboxer

Запуск **нескольких автономных кодинг-агентов параллельно** (или ручная работа в одной
песочнице), каждый в своей изоляции, на **локальной Linux-машине**. Конфиг-driven через
Nix-профили; поставка — nix flake.

Каждая песочница — **отдельная директория-копия** проекта (rsync `mainSrc`) + git-ветка
`sandbox/<slug>` + snapshot стартового состояния. Возврат результатов — обратно в исходный
репозиторий через git (cherry-pick диапазона `snapshot..tip`) либо `--patch`.

Статус: **рабочий MVP**. Два бэкенда изоляции:

- **native** — нативный Claude Code `/sandbox` (bubblewrap, FS+сеть на уровне ОС). Только `claude`,
  нулевая установка, проверено на NixOS из коробки.
- **podman / docker** — контейнер-тулбокс с JS-агентами (claude, opencode, aider, pi, gemini).
  Любой из них; сеть/прокси/креды прокидываются per-config. `codex` (Rust) в образ не запекается
  из-за времени сборки — используйте его на нативном бэкенде.

## Требования

- Параллельный запуск N агентов, изоляция **на агента**.
- Локальная Linux-машина, доверенный код → namespace-песочницы достаточно; microVM не нужен.
- Установлен и авторизован нужный агент: `claude` для native-бэкенда; `podman`/`docker` на хосте
  для контейнерного. Бинарь `claude` и движок контейнеров **не бандлятся** — берутся с хоста.

## Установка (nix flake)

Linux-only. `claude` (для native) не бандлится (проприетарный); контейнерный движок — с хоста.

```bash
# разово, без установки
nix run github:<owner>/sandboxer -- help          # или: nix run .#sandboxer -- ...

# в профиль (CLI `sandboxer` в PATH)
nix profile install github:<owner>/sandboxer

# как вход в другой flake
inputs.sandboxer.url = "github:<owner>/sandboxer";
# затем: environment.systemPackages = [ inputs.sandboxer.packages.${system}.default ];

# локально из этого репо
nix build .#sandboxer && ./result/bin/sandboxer help
nix develop                                         # dev-shell с зависимостями
```

Обёртка кладёт в PATH зависимости (bash, coreutils, git, rsync, nodejs, jq, bubblewrap, socat),
**сохраняя PATH хоста**, чтобы `claude`, `podman`/`docker`, `nix` и `systemd-run` оставались доступны.

Контейнерный бэкенд требует образ-тулбокс (один раз, и при обновлении агентов):

```bash
nix run .#build-image          # собрать образ и загрузить в podman/docker (host systemd не трогается)
nix flake update llm-agents    # обновить набор агентов; пересобирает только слои агентов
```

## Команды

```
ЖИЗНЕННЫЙ ЦИКЛ
  sandboxer create [<slug>|<f.nix>|--config f] [--src DIR] [--allow-domains a,b]
  sandboxer enter  [<slug>|--config f] [--backend podman|native]      # шелл внутри песочницы
  sandboxer exec   [<slug>] [--backend B] -- <cmd...>                  # выполнить команду внутри
  sandboxer run    [tasks] [--config f] [--agent X] [--model M] [--backend B]
                   [--max-parallel N] [--nice N] [--mem 2G] [--cpu 100%] [--wall SEC]
                   [--allow-domains a,b] [--dry-run] [--keep]          # батч автономных агентов

ЗАВИСИМОСТИ / ВОЗВРАТ
  sandboxer pull  [<slug>] [--force]     # origins -> песочница (skip изменённых; --force затирает)
  sandboxer push  [<slug>] [--force]     # rw-зависимости песочница -> origins
  sandboxer merge [slug...] [--patch]    # вернуть КОД в исходный репо (cherry-pick snapshot..tip)

ОСМОТР / УБОРКА
  sandboxer list | diff [<slug>] | show [<slug>]
  sandboxer rm <slug> | rm-all [SRC]
  sandboxer use [<slug>] [--clear]       # выбрать активную песочницу (* в list)
  sandboxer agents                       # реестр агентов: bin, sandbox-режим, креды
```

Выбор песочницы снаружи: `-S/--sandbox <slug>`, `sandboxer use <slug>` или позиционно; внутри
контейнера — автоматически (разрешены только `pull/push/show/list/diff`).

## Профили (Nix)

Профиль описывает песочницу декларативно. `sandboxer` подхватывает `sandboxer.nix` из cwd
автоматически, либо `--config feat.nix`, либо позиционно `sandboxer create feat.nix`. См.
`examples/sandboxer.nix` (минимальный) и `examples/with-deps.nix` (зависимости, прокси, контейнер).

```nix
{
  name = "feature-x";                 # slug: каталог .sandboxer/<name>, ветка sandbox/<name>
  mainSrc = "./.";                    # что копируется (rsync) в песочницу
  backend = "native";                 # native | podman | docker
  agent = "claude";                   # claude | codex | opencode | aider | pi | gemini
  # model = "sonnet";
  network.allowedDomains = [ "api.anthropic.com" "registry.npmjs.org" "github.com" ];

  # agents = [ "opencode" "claude" ];                    # какие креды биндить в контейнер
  # proxy = { http = "http://proxy:3128"; https = "http://proxy:3128"; no = "localhost"; };
  # srcs = [ { from = "/abs/shared-lib"; to = "vendor/shared-lib"; mode = "rw"; }
  #          { root = "/abs/schemas"; glob = "**/*.proto"; to = "proto"; mode = "ro"; } ];
  # extraMounts = [ { source = "/data"; target = "/data"; mode = "rw"; } ];
  # env = { NPM_CONFIG_REGISTRY = "https://registry.npmjs.org/"; };
}
```

**Зависимости (`srcs`)** — внешние каталоги/файлы, втягиваемые ВНУТРЬ песочницы (`pull`) и, для
`mode = "rw"`, возвращаемые в origin (`push`). Запись — либо явная (`from`/`to`), либо матчер
(`root` + `name`|`glob`|`regex`, опц. `depth`). `pull`/`push` детектят локальные изменения по
stat-сигнатуре и по умолчанию не затирают расходящиеся цели (`--force` затирает).

## Батч (tasks-файл)

```bash
cd your-project                      # git-репо
sandboxer run tasks.txt --backend native --model sonnet --max-parallel 3
sandboxer list                       # таблица: sandbox / exit / sec / changed / result
sandboxer diff [slug]                # дифф изменений агента (чисто, относительно snapshot)
sandboxer merge [slug...]            # cherry-pick коммитов агента в текущую ветку (или --patch)
```

Формат (`sandboxer.tasks.example`): `[slug]` на отдельной строке, далее многострочный промпт до
следующего `[slug]`; строки с `#` игнорируются.

## Как работает

На каждый `[slug]` / `create`:

1. **Копия** проекта в `<src>/.sandboxer/<slug>` (rsync, без `.sandboxer`/`node_modules`/`sandboxer.tasks`);
   ветка `sandbox/<slug>` + snapshot. Если есть профиль — `srcs` втягиваются внутрь снапшота.
2. **Запуск агента** в выбранном бэкенде:
   - *native:* `claude -p … --permission-mode bypassPermissions --settings '<inline sandbox json>'` —
     запись вне копии режет ОС (`read-only file system`), сеть по `allowedDomains`. В репо ничего не пишется.
   - *контейнер:* агент из образа-тулбокса; `--user $(id -u):$(id -g) --cap-drop=ALL
     --security-opt no-new-privileges`, HOME = хостовый (тот же путь), креды агента биндятся
     (в батче — эфемерная копия), origins зависимостей — bind по mode (rw/ro). **Egress-allowlist:**
     агент в `--internal` сети (без интернета), единственный выход — forward-proxy `gost` в сайдкаре,
     пропускающий CONNECT/HTTP только к `network.allowedDomains` (whitelist), остальное → 403.
3. **Параллелизм** — семафор `--max-parallel`; политес — `nice`; опц. `--wall` (timeout),
   `--mem`/`--cpu` (cgroup через transient `systemd-run --user --scope`, per-user, по умолчанию выкл).
4. **Возврат** — `merge` cherry-pick'ит диапазон `snapshot..tip` в исходный репо (или `--patch`
   выгружает `.patch`); `push` возвращает rw-зависимости в их origins.

Реестр агентов — `agents/registry.nix` (один источник правды для лаунчера, сборки образа и
авторизации). Добавить агента = добавить одну запись. Резолвер профилей и перенос зависимостей —
`libexec/sandboxer-cfg.mjs`.

**Egress в контейнере** форсируется gost-сайдкаром (см. выше). Отключить — `SANDBOXER_NO_EGRESS=1`
или `egress = false;` в профиле; если задан `proxy.*`, границу держит указанный upstream-прокси, а
сайдкар не поднимается. Native-бэкенд использует нативный `/sandbox` (allowlist на уровне ОС).

### Известные ограничения

- Egress-allowlist домен-уровневый (по hostname в CONNECT/HTTP), не per-path/IP; жёсткий kill
  процесса sandboxer (SIGKILL) может оставить сети `sbx-*` — чистить `docker network prune`.
- Адверсариальные «выйди из песочницы» промпты агент отклоняет на уровне модели — границу ОС
  проверять **легитимно сформулированным** тестом записи в `$HOME` (агент репортит `read-only file system`).

## Факты хоста (проверено)

| Компонент | Значение |
|---|---|
| `claude` | 2.1.156 — нативные `/sandbox` и worktrees |
| `git` | 2.50.1 |
| `docker` | 27.5.1 |
| `bwrap` (bubblewrap) | 0.11.2 |
| `node` | v22.20.0 |
| unprivileged userns | включены (`max_user_namespaces=110758`) |
| cgroup v2 | присутствует (лимиты ресурсов) |
| ОС | **NixOS** (`/nix/store`, `/run/current-system`) |

**NixOS-нюанс:** native-бэкенд работает из коробки (весь FS ro → `/nix/store` читается).
Контейнерный бэкенд от FHS-различий абстрагирован образом.

## Структура

```
bin/sandboxer            — лаунчер (bash): create/enter/exec/run/pull/push/merge/list/diff/show/rm/use/agents
libexec/sandboxer-cfg.mjs — резолвер Nix-профилей (.nix→JSON) + перенос зависимостей (in/out)
agents/registry.nix       — декларативный реестр агентов (лаунчер + образ + авторизация)
flake.nix                 — package + apps (sandboxer, build-image) + devShell + OCI-образ
examples/                 — sandboxer.nix (минимальный), with-deps.nix (полный)
sandboxer.tasks.example   — пример tasks-файла для `run`
docs/market-research.md   — карта рынка, что отброшено и почему
eval/matrix.md            — оценка трёх PoC-кандидатов
poc/                      — PoC native / srt / docker + стендовый репо
```

## Decision-log

- **2026-05-29** — Greenfield. Требования: параллельные агенты, локально, доверенный код,
  namespace-изоляция. Двухшаговый подход: PoC → build-or-adopt.
- **2026-05-29 — РЕШЕНИЕ: adopt native + тонкий оркестратор. Свою песочницу НЕ пишем.** Прогнаны
  3 PoC (`eval/matrix.md`), баллы 54/44/46. **Native** (`/sandbox` + headless `claude
  --permission-mode bypassPermissions`) закрывает все требования без кода песочницы, на NixOS из
  коробки. **srt** — отличная FS-изоляция, но сеть упёрлась в systemd/proxy. **Docker-per-agent** —
  сильнейшая граница, NixOS-agnostic, но требует агента-в-образе + авторизацию.
- **2026-05-29 — Фаза 3: MVP `bin/sandboxer`.** Изоляция на агента = отдельная директория-копия
  (rsync, НЕ git worktree); возврат = git (snapshot + cherry-pick); поставка только nix/flake;
  systemd — только в установленном инструменте, не ad-hoc по хосту. Проверено e2e через `nix build`.
- **2026-05-31 — Фаза 4: конфиг-driven, мульти-агент, два бэкенда.** Native перестал быть
  единственным: добавлен контейнерный бэкенд (podman/docker) с образом-тулбоксом всех агентов
  (`numtide/llm-agents.nix`), декларативный реестр агентов, Nix-профили (`mainSrc`/`srcs`/`proxy`/
  `network`/`agent`/`model`/`backend`), перенос зависимостей `pull`/`push` (rw/ro, stat-детект),
  ручной цикл `create`/`enter`/`exec`. Команда `clean` → `rm`/`rm-all`. **Egress-allowlist в
  контейнере**: агент в `--internal` сети + forward-proxy `gost` v3 (whitelist-bypass по
  `allowedDomains`) — выбран вместо tinyproxy. Также исправлен Entrypoint образа (`/bin/bash` →
  `Cmd`), иначе контейнер не исполнял переданную команду. Проверено: native create/run/exec/diff/
  merge, pull/push rw·ro, eval образа и профилей, gost allowlist (allowed→200 / blocked→403).
