# sandboxer

[![ci](https://github.com/irasikhin/sandboxer/actions/workflows/ci.yml/badge.svg)](https://github.com/irasikhin/sandboxer/actions/workflows/ci.yml)

Запуск **нескольких автономных кодинг-агентов параллельно** (или ручная работа в одной
песочнице), каждый в своей изоляции, на **локальной Linux-машине**. CLI на Go; поставка — готовый
бинарь, `go install` или nix flake.

Каждая песочница — **отдельная директория-копия** проекта (rsync `mainSrc`) + git-ветка
`sandbox/<slug>` + snapshot стартового состояния. Возврат результатов — обратно в исходный
репозиторий через git (`cherry-pick` диапазона `snapshot..tip`) либо `--patch`.

Два бэкенда изоляции:

- **native** — нативный Claude Code `/sandbox` (bubblewrap, FS+сеть на уровне ОС). Только `claude`,
  нулевая установка.
- **podman / docker** — контейнер-тулбокс со встроенными агентами (claude, opencode, crush, aider,
  pi, gemini). Любой из них; сеть/прокси/креды прокидываются per-config. `codex` (Rust) в образ не
  запекается из-за времени сборки — используйте его на нативном бэкенде.

## Установка

Linux-only. `claude` (для native) и движок контейнеров **не бандлятся** — берутся с хоста.

```bash
# nix (без установки / в профиль / как flake-вход)
nix run    github:irasikhin/sandboxer -- help
nix profile install github:irasikhin/sandboxer

# go
go install github.com/irasikhin/sandboxer/cmd/sandboxer@latest

# либо скачать бинарь из GitHub Releases (linux amd64/arm64)
```

## Быстрый старт

```bash
sandboxer create feat              # копия проекта + ветка sandbox/feat
sandboxer enter  feat              # интерактивный шелл внутри (агенты в PATH)
sandboxer exec   feat -- claude    # запустить агента/команду внутри
sandboxer merge  feat              # вернуть код в исходный репозиторий (cherry-pick)
sandboxer list                     # статус песочниц
sandboxer rm     feat              # удалить песочницу
```

Параллельный батч автономных агентов (по одной песочнице на задачу):

```bash
sandboxer run tasks.txt --agent claude --max-parallel 4
# tasks-файл: секции [slug] + текст задачи (см. sandboxer.tasks.example)
```

## Конфигурация

Скаляры задаются **флагами** и переменными окружения `SANDBOXER_*`:

| Что | Флаг | Env |
|-----|------|-----|
| агент | `--agent` | `SANDBOXER_AGENT` (по умолчанию `claude`) |
| бэкенд | `--backend` | `SANDBOXER_BACKEND` (по умолчанию `podman`) |
| модель | `--model` | `SANDBOXER_MODEL` |
| egress-домены | `--allow-domains a,b` | `SANDBOXER_DOMAINS` |
| образ | — | `SANDBOXER_IMAGE` (по умолчанию `sandboxer-toolbox:latest`) |

Структурные поля (вендоринг зависимостей `srcs`, `extraMounts`, `env`) — в **опциональном**
`sandboxer.yaml` (автоподхват в cwd или `--config <file>`). См. `examples/sandboxer.yaml` и
`examples/with-deps.yaml`.

```yaml
name: feature-x
mainSrc: .
backend: native
agent: claude
network:
  allowedDomains: [api.anthropic.com, registry.npmjs.org, github.com]
srcs:
  - { from: /abs/shared-lib, to: vendor/shared-lib, mode: rw }   # вернётся в origin при push
  - { root: /abs/schemas, glob: "**/*.proto", to: proto, mode: ro }
```

`srcs` втягиваются внутрь песочницы (`sandboxer pull`); rw-записи возвращаются в origin
(`sandboxer push`), с защитой от затирания локальных изменений (без `--force`).

## Агенты

```bash
sandboxer agents     # каталог: bin, sandbox-режим, попадание в образ, какие креды/env биндить
```

Реестр — единый источник `internal/registry/registry.json` (его же читает flake для образа).
Добавить агента = одна запись.

## Egress-allowlist (контейнерный бэкенд)

Агент сидит в `--internal` сети без прямого выхода; единственный выход — allowlist-прокси,
пропускающий только домены из `network.allowedDomains` (остальное → 403). Прокси — **тот же бинарь**
в скрытом режиме `sandboxer _proxy` (внешних зависимостей нет). Отключить: `egress: false` в профиле
или `SANDBOXER_NO_EGRESS=1`. Заданный upstream-прокси держит границу сам.

## Образ-тулбокс

```bash
nix run .#build-image     # собрать OCI-образ с агентами + бинарём и загрузить в podman/docker
```

## Разработка

```bash
nix develop               # go toolchain + линтеры
go build ./cmd/sandboxer
go test ./...
golangci-lint run ./...
nix flake check
```

См. [CONTRIBUTING.md](./CONTRIBUTING.md) (Conventional Commits, релиз) и
[SECURITY.md](./SECURITY.md) (модель изоляции).
