# Минимальный профиль песочницы.
#
# sandboxer ищет `sandboxer.nix` в текущем каталоге автоматически (или укажи явно:
# `sandboxer create --config путь/feat.nix`, либо позиционно `sandboxer create feat.nix`).
#
# Профиль — это Nix-выражение: либо attrset (как здесь), либо функция `{ ... }: { ... }`.
# Вычисляется через `nix eval --impure --json`, результат — JSON для лаунчера.
{
  # Имя песочницы (slug). Из него получаются: каталог .sandboxer/<name>, ветка sandbox/<name>.
  name = "feature-x";

  # Корень проекта, который копируется (rsync) в песочницу. По умолчанию — каталог профиля.
  mainSrc = "./.";

  # Бэкенд изоляции:
  #   "podman" | "docker" — контейнер-тулбокс (любой агент); сеть/креды прокидываются per-config;
  #   "native"            — нативный Claude /sandbox (только agent = "claude").
  backend = "native";

  # Кодинг-агент из реестра (sandboxer agents): claude | codex | opencode | crush | aider | pi | gemini.
  agent = "claude";

  # Модель агента (необязательно). Для claude: "haiku" | "sonnet" | "opus" | полный id.
  # model = "sonnet";

  # Egress-allowlist. В native-бэкенде — это allowedDomains нативного /sandbox.
  network.allowedDomains = [
    "api.anthropic.com"
    "registry.npmjs.org"
    "github.com"
  ];
}
