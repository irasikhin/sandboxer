# agents/registry.nix — декларативный реестр агентов.
#
# Один источник правды для трёх потребителей:
#   1. лаунчер bin/sandboxer  — name -> bin + шаблоны команд (interactive/headless);
#   2. сборка образа flake.nix — какие .package запекать в слой агентов;
#   3. авторизация            — какие конфиг-директории биндить и какие env пробрасывать.
#
# Вызывается двумя способами:
#   (import ./registry.nix {})                      # только метаданные (package = null) — для
#                                                   # `nix eval --json` из bin/sandboxer и `agents`;
#   import ./registry.nix { inherit pkgs llmAgents } # с пакетами — для dockerTools-образа.
#
# Поля записи:
#   package         — derivation агента (из numtide/llm-agents.nix; null в режиме метаданных);
#   bin             — имя бинаря в PATH;
#   interactive     — шаблон интерактивного запуска ({settingsFlag}/{modelFlag} подставляет лаунчер);
#   headless        — шаблон headless-запуска ({task}/{modelFlag}/{settingsFlag});
#   nativeSandbox   — true только у claude (понимает `--settings '{sandbox…}'`); у прочих сеть
#                     обеспечивает контейнер/прокси, а {settingsFlag} подставляется пустым;
#   authConfigDirs  — [{ path; mode; optional? }] хостовые конфиги для bind (HOME в контейнере = хостовый);
#   authEnv         — имена env-переменных с ключами (пробрасываются, если заданы на хосте).
#
# Расширяемость: добавить агента = добавить одну запись. `deepseek-reasoner` — это МОДЕЛЬ,
# а не агент: используйте agent = "opencode"|"pi" + model = "deepseek/deepseek-reasoner"
# (оба запекаются в образ, у обоих DEEPSEEK_API_KEY в authEnv). aider тоже умеет deepseek, но
# в llm-agents для этой платформы пакета нет (package=null) — в образ не попадает.

{ pkgs ? null, llmAgents ? null }:

let
  pkg = name: if llmAgents == null then null else llmAgents.${name} or null;
in
{
  claude = {
    package        = pkg "claude-code";
    bin            = "claude";
    interactive    = "claude {settingsFlag} {modelFlag}";
    headless       = "claude -p {task} --permission-mode bypassPermissions {modelFlag} {settingsFlag} --output-format json";
    nativeSandbox  = true;
    authConfigDirs = [
      { path = "~/.claude"; mode = "rw"; }
      { path = "~/.config/anthropic"; mode = "rw"; optional = true; }
    ];
    authEnv = [ "ANTHROPIC_API_KEY" "CLAUDE_CODE_OAUTH_TOKEN" "ANTHROPIC_BASE_URL" ];
  };

  codex = {
    package        = pkg "codex";
    bin            = "codex";
    interactive    = "codex {modelFlag}";
    headless       = "codex exec {task}";
    nativeSandbox  = false;
    authConfigDirs = [ { path = "~/.codex"; mode = "rw"; } ];
    authEnv = [ "OPENAI_API_KEY" "CODEX_API_KEY" ];
  };

  opencode = {
    package        = pkg "opencode";
    bin            = "opencode";
    interactive    = "opencode {modelFlag}";
    headless       = "opencode run {task}";
    nativeSandbox  = false;
    authConfigDirs = [
      { path = "~/.local/share/opencode"; mode = "rw"; }       # auth.json (ротация)
      { path = "~/.config/opencode"; mode = "ro"; optional = true; }
    ];
    authEnv = [ "OPENAI_API_KEY" "ANTHROPIC_API_KEY" "OPENROUTER_API_KEY" "DEEPSEEK_API_KEY" ];
  };

  aider = {
    package        = pkg "aider";
    bin            = "aider";
    interactive    = "aider {modelFlag}";
    headless       = "aider --message {task} --yes-always {modelFlag}";
    nativeSandbox  = false;
    authConfigDirs = [
      { path = "~/.aider.conf.yml"; mode = "ro"; optional = true; }
      { path = "~/.aider.model.settings.yml"; mode = "ro"; optional = true; }
    ];
    authEnv = [ "OPENAI_API_KEY" "ANTHROPIC_API_KEY" "DEEPSEEK_API_KEY" "OPENROUTER_API_KEY" ];
  };

  pi = {
    package        = pkg "pi";                                  # @earendil-works/pi-coding-agent
    bin            = "pi";
    interactive    = "pi {modelFlag}";
    headless       = "pi {task}";                               # TO-VERIFY headless-флаг
    nativeSandbox  = false;
    authConfigDirs = [ { path = "~/.pi"; mode = "rw"; } ];
    authEnv = [ "ANTHROPIC_API_KEY" "OPENAI_API_KEY" "DEEPSEEK_API_KEY" ];
  };

  gemini = {
    package        = pkg "gemini-cli";
    bin            = "gemini";
    interactive    = "gemini {modelFlag}";
    headless       = "gemini -p {task}";
    nativeSandbox  = false;
    authConfigDirs = [ { path = "~/.gemini"; mode = "rw"; } ];
    authEnv = [ "GEMINI_API_KEY" "GOOGLE_API_KEY" ];
  };
}
