# Профиль с зависимостями, контейнерным бэкендом и прокси.
#
# Показывает полный набор полей. `srcs` — внешние каталоги/файлы, которые втягиваются ВНУТРЬ
# песочницы (pull) и, для rw, возвращаются обратно в origin (push). Удобно для монорепо-вендоринга
# или общих либ, лежащих вне mainSrc.
{
  name = "integ";
  mainSrc = "/home/me/work/app";

  # Контейнер-тулбокс: образ с JS-агентами (nix run .#build-image). Любой агент, не только claude.
  # (codex — Rust, в образ не запекается из-за времени сборки; на контейнерном бэкенде недоступен,
  #  используйте его на нативном бэкенде. См. flake.nix imageAgents.)
  backend = "podman";
  agent = "opencode";
  model = "gpt-5";

  # Какие агенты авторизовать в контейнере (биндить их конфиг-каталоги/env). По умолчанию — все
  # из реестра; сузить полезно, чтобы не монтировать лишние креды.
  agents = [ "opencode" "claude" ];

  network.allowedDomains = [ "api.openai.com" "api.anthropic.com" "registry.npmjs.org" "pypi.org" ];

  # Egress в контейнере по умолчанию форсируется gost-сайдкаром (агент в --internal сети, наружу
  # только перечисленные домены). Выключить: egress = false; (или env SANDBOXER_NO_EGRESS=1).
  # egress = false;

  # Корпоративный прокси (прокидывается как HTTP(S)_PROXY/NO_PROXY и в native-, и в контейнер-бэкенд).
  # proxy.http  = "http://proxy.corp:3128";
  # proxy.https = "http://proxy.corp:3128";
  # proxy.no    = "localhost,127.0.0.1,.corp";

  # Зависимости: каждая запись — либо ЯВНАЯ (from/to), либо МАТЧЕР (root + name|glob|regex).
  srcs = [
    # Явная: каталог-вендор, изменения возвращаются в origin при push.
    { from = "/home/me/work/shared-lib"; to = "vendor/shared-lib"; mode = "rw"; }
    # Явная ro: справочные данные, обратно не пишутся.
    { from = "/home/me/work/fixtures"; to = "test/fixtures"; mode = "ro"; }
    # Матчер: втянуть все *.proto из каталога схем (только чтение).
    { root = "/home/me/work/schemas"; glob = "**/*.proto"; to = "proto"; mode = "ro"; }
  ];

  # Доп. бинд-монты и env для контейнера (необязательно).
  # extraMounts = [ { source = "/data/cache"; target = "/data/cache"; mode = "rw"; } ];
  # env = { NPM_CONFIG_REGISTRY = "https://registry.npmjs.org/"; };
}
