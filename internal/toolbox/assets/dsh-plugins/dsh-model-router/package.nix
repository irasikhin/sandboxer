# dsh-model-router — role-based model routing for dsh: the planner (root)
# agent runs on deepseek-v4-pro, every delegated executor subagent on
# deepseek-v4.1-flash. Vendored like dshmarket: copied as a REAL dir into
# dsh's node_modules, peers stripped (cordis, schemastery — resolve at
# runtime from the dsh installation).
#
# Zero runtime dependencies, so no npm install at all: a plain unpack keeps
# exactly what the published tarball ships (lib/, the bundle patch, the
# pro-flash-routing skill).
#
# DEEPSEEK-V4.1-FLASH DAY-ONE PATCH (see installPhase): the published 0.6.3
# still routes executors to deepseek-v4-flash and lists only the v4 models in
# the deepseek catalog. The image patches three things so a sandbox supports
# v4.1 the day it ships, without waiting for upstream:
#   - the bundle patch's `model-router` row — executor route -> v4.1-flash
#     (the row config in the bundle patch is what actually configures the
#     router; the plugin code default only covers rows inserted bare);
#   - the bundle patch's `llm-deepseek` row — v4.1-flash added to the model
#     catalog, which IS the model picker;
#   - the advisory prose (the registered pro-flash-routing skill, both the
#     embedded copy and skills/ SKILL.md), which tells the planner which
#     model the executors run on — it must not keep saying v4-flash.
# Every substitution is --replace-fail: when upstream adopts v4.1, the strings
# change and the build FAILS here instead of silently double-patching — that
# failure is the signal to drop the patch.
#
# COMPATIBILITY BRIDGE onto dsh >= 0.1.2 (see installPhase): the published
# plugin targets the pre-0.1.2 harness API. The harness 0.1.2 rewrite removed
# the plugin's imported `foldPlanMode` (dsh-plan-mode) and
# `installSettingsSection`/`settingsNamespace` (dsh-settings), so the plugin
# cannot even load unpatched on the dsh release this image bakes. The bridge
# stubs exactly those three symbols; what remains:
#   - strict routing (the default mode), vision routing, error escalation,
#     the prompt section and the pro-flash-routing skill all work — those
#     ride cordis/agent/request/systemPrompt/skills, whose 0.1.2 surface is
#     unchanged;
#   - mode:"plan" degrades to strict (plan-mode state projection was
#     rewritten; the stub returns "not active");
#   - the browser settings card is absent (the plugin's dsh.client half
#     targets the old client host and names packages dsh 0.1.2 does not
#     carry — dsh.client is stripped so no broken row is served). Disable or
#     retune the router via the profile's own cordis.patch.yml layer, which
#     applies after the plugin's bundle patch.
# The substitutions use --replace-fail: when upstream ships a release that
# targets the 0.1.2 API, the import lines change and the build FAILS here
# instead of silently shipping the stale bridge — that failure is the signal
# to bump and drop the patch.
{
  lib,
  stdenv,
  fetchurl,
  jq,
}:

let
  version = "0.6.3";
  sourceHash = "sha256-b2LA4SHxhYH3py/PfJriSf3i5AUrx5s5TgqQcasTkX8=";
in
stdenv.mkDerivation {
  pname = "dsh-model-router";
  inherit version;

  src = fetchurl {
    url = "https://registry.npmjs.org/dsh-model-router/-/dsh-model-router-${version}.tgz";
    hash = sourceHash;
  };

  nativeBuildInputs = [ jq ];

  unpackPhase = ''
    runHook preUnpack
    mkdir -p package
    tar -xzf "$src" -C package --strip-components=1
    runHook postUnpack
  '';

  installPhase = ''
    runHook preInstall
    jq 'del(.devDependencies, .peerDependencies, .dsh.client)' package/package.json > package/package.json.stripped
    mv package/package.json.stripped package/package.json
    # The compatibility bridge — see the file header.
    substituteInPlace package/lib/index.js \
      --replace-fail \
        'import { foldPlanMode } from "@deepseek-ai/dsh-plan-mode";' \
        'const foldPlanMode = () => false; // dsh >= 0.1.2: plan-mode projection rewritten; mode:"plan" degrades to strict' \
      --replace-fail \
        'import { installSettingsSection, settingsNamespace } from "@deepseek-ai/dsh-settings";' \
        'const settingsNamespace = (ns) => ns; const installSettingsSection = () => () => {}; // dsh >= 0.1.2: settings sections live in SettingsProvider'
    # deepseek-v4.1-flash day-one patch — see the file header.
    substituteInPlace package/cordis.patch.yml \
      --replace-fail \
        '          model: deepseek-v4-flash' \
        '          model: deepseek-v4.1-flash' \
      --replace-fail \
        '      - id: deepseek-v4-flash
            name: DeepSeek-V4-Flash
            contextWindow: 1000000
            maxTokens: 256000' \
        '      - id: deepseek-v4-flash
            name: DeepSeek-V4-Flash
            contextWindow: 1000000
            maxTokens: 256000
          - id: deepseek-v4.1-flash
            name: DeepSeek-V4.1-Flash
            contextWindow: 1000000
            maxTokens: 256000'
    substituteInPlace package/lib/index.js \
      --replace-fail \
        'model: "deepseek-v4-flash"' \
        'model: "deepseek-v4.1-flash"' \
      --replace-fail \
        'deepseek-v4-flash\`. Implementation work' \
        'deepseek-v4.1-flash\`. Implementation work' \
      --replace-fail \
        'automatically routed to \`deepseek-v4-flash\`' \
        'automatically routed to \`deepseek-v4.1-flash\`' \
      --replace-fail \
        'produced by \`deepseek-v4-flash\`;' \
        'produced by \`deepseek-v4.1-flash\`;'
    substituteInPlace package/skills/pro-flash-routing/SKILL.md \
      --replace-fail \
        '`deepseek-v4-flash`. Implementation work' \
        '`deepseek-v4.1-flash`. Implementation work' \
      --replace-fail \
        'automatically routed to `deepseek-v4-flash`' \
        'automatically routed to `deepseek-v4.1-flash`' \
      --replace-fail \
        'produced by `deepseek-v4-flash`;' \
        'produced by `deepseek-v4.1-flash`;'
    mkdir -p "$out/lib/node_modules"
    mv package "$out/lib/node_modules/dsh-model-router"
    runHook postInstall
  '';

  meta = {
    description = "dsh plugin: role-based model routing — planner on deepseek-v4-pro, executor subagents on deepseek-v4.1-flash";
    homepage = "https://github.com/thedeveloper256/dsh-model-router";
    license = lib.licenses.mit;
  };
}
