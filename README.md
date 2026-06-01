# sandboxer

[![CI](https://github.com/irasikhin/sandboxer/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/irasikhin/sandboxer/actions/workflows/ci.yml?query=branch%3Amain)
[![Coverage](https://img.shields.io/badge/coverage-92.1%25-brightgreen.svg)](#разработка)
[![Go](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

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
go test ./... -cover                      # покрытие по пакетам
go test -coverprofile=cov.out ./... && go tool cover -func=cov.out | tail -1   # суммарно
golangci-lint run ./...
nix flake check
```

Покрытие тестами: **92.1%** суммарно. По пакетам:

| Пакет                  | Покрытие |
| ---------------------- | -------- |
| `cmd/sandboxer`        | 100.0%   |
| `internal/config`      | 100.0%   |
| `internal/gitx`        | 95.2%    |
| `internal/backend`     | 94.9%    |
| `internal/runner`      | 94.8%    |
| `internal/egress`      | 94.3%    |
| `internal/registry`    | 94.1%    |
| `internal/cli`         | 90.9%    |
| `internal/proxy`       | 90.3%    |
| `internal/srcs`        | 90.3%    |
| `internal/sandbox`     | 88.5%    |

Тесты на бэкенды используют фейковые движки (скрипты-заглушки `podman`/`claude` в
`PATH`) и изолированный git-конфиг, поэтому проходят без контейнеров; те, что требуют
`git`/`rsync`, аккуратно пропускаются, если инструмент недоступен.

См. [CONTRIBUTING.md](./CONTRIBUTING.md) (Conventional Commits, релиз) и
[SECURITY.md](./SECURITY.md) (модель изоляции).

## Лицензия

MIT — см. [LICENSE](./LICENSE).
