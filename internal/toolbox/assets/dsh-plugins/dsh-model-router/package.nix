# dsh-model-router — role-based model routing for dsh: the planner (root)
# agent and every delegated executor subagent run on DeepSeek V4.1 Flash.
# Vendored like dshmarket: copied as a REAL dir into
# dsh's node_modules, peers stripped (cordis, schemastery — resolve at
# runtime from the dsh installation).
#
# Zero runtime dependencies, so no npm install at all: a plain unpack keeps
# exactly what the published tarball ships (lib/, the bundle patch, the
# pro-flash-routing skill).
#
# DEEPSEEK V4.1 FLASH PATCH (see installPhase): the published 0.6.3 routes the
# planner to deepseek-v4-pro and executors to deepseek-v4-flash, and lists only
# the v4 models in the deepseek catalog. The image patches three things:
#   - the bundle patch's `model-router` row — BOTH routes (planner and
#     executor) -> DeepSeek V4.1 Flash (the row config in the bundle patch is
#     what actually configures the router; the plugin code default only
#     covers rows inserted bare);
#   - the bundle patch's `llm-deepseek` row — the model added to the catalog,
#     which IS the model picker;
#   - the advisory prose (the registered pro-flash-routing skill, both the
#     embedded copy and skills/ SKILL.md), which names the models per role.
# THE ID IS THE BETA ONE, AND IT EXPIRES: api.deepseek.com serves
# `deepseek-v4.1-flash-expires-on-0910` (verified HTTP 200 on 2026-09-09) but
# NOT the permanent `deepseek-v4.1-flash` (HTTP 400) — V4.1 Flash launches
# around 2026-09-10 (Beijing time). When the permanent id goes live, swap it
# here (and in sandbox.PiModels) with a follow-up patch release; the beta id
# stops working on 09-10, so a release built with it MUST be superseded then.
# Every substitution is --replace-fail: when upstream adopts V4.1, the strings
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
    # DeepSeek V4.1 Flash patch — see the file header. The WORKING id today is
    # the beta `deepseek-v4.1-flash-expires-on-0910` (HTTP 200 against
    # api.deepseek.com, 2026-09-09); the permanent `deepseek-v4.1-flash` is not
    # served yet. Both router roles and the catalog move onto it; the beta id
    # expires 2026-09-10, so the id is the ONE thing to swap then.
    substituteInPlace package/cordis.patch.yml \
      --replace-fail \
        '          model: deepseek-v4-pro' \
        '          model: deepseek-v4.1-flash-expires-on-0910' \
      --replace-fail \
        '          model: deepseek-v4-flash' \
        '          model: deepseek-v4.1-flash-expires-on-0910' \
      --replace-fail \
        '      - id: deepseek-v4-flash
            name: DeepSeek-V4-Flash
            contextWindow: 1000000
            maxTokens: 256000' \
        '      - id: deepseek-v4-flash
            name: DeepSeek-V4-Flash
            contextWindow: 1000000
            maxTokens: 256000
          - id: deepseek-v4.1-flash-expires-on-0910
            name: DeepSeek-V4.1-Flash (beta, expires 09-10)
            contextWindow: 1000000
            maxTokens: 256000'
    substituteInPlace package/lib/index.js \
      --replace-fail \
        'model: "deepseek-v4-pro"' \
        'model: "deepseek-v4.1-flash-expires-on-0910"' \
      --replace-fail \
        'model: "deepseek-v4-flash"' \
        'model: "deepseek-v4.1-flash-expires-on-0910"' \
      --replace-fail \
        '\`deepseek-v4-pro\`. Planning' \
        '\`deepseek-v4.1-flash-expires-on-0910\`. Planning' \
      --replace-fail \
        'planner output by \`deepseek-v4-pro\`' \
        'planner output by \`deepseek-v4.1-flash-expires-on-0910\`' \
      --replace-fail \
        'deepseek-v4-flash\`. Implementation work' \
        'deepseek-v4.1-flash-expires-on-0910\`. Implementation work' \
      --replace-fail \
        'automatically routed to \`deepseek-v4-flash\`' \
        'automatically routed to \`deepseek-v4.1-flash-expires-on-0910\`' \
      --replace-fail \
        'produced by \`deepseek-v4-flash\`;' \
        'produced by \`deepseek-v4.1-flash-expires-on-0910\`;'
    substituteInPlace package/skills/pro-flash-routing/SKILL.md \
      --replace-fail \
        '`deepseek-v4-pro`. Planning' \
        '`deepseek-v4.1-flash-expires-on-0910`. Planning' \
      --replace-fail \
        'planner output by `deepseek-v4-pro`' \
        'planner output by `deepseek-v4.1-flash-expires-on-0910`' \
      --replace-fail \
        '`deepseek-v4-flash`. Implementation work' \
        '`deepseek-v4.1-flash-expires-on-0910`. Implementation work' \
      --replace-fail \
        'automatically routed to `deepseek-v4-flash`' \
        'automatically routed to `deepseek-v4.1-flash-expires-on-0910`' \
      --replace-fail \
        'produced by `deepseek-v4-flash`;' \
        'produced by `deepseek-v4.1-flash-expires-on-0910`;'
    mkdir -p "$out/lib/node_modules"
    mv package "$out/lib/node_modules/dsh-model-router"
    runHook postInstall
  '';

  meta = {
    description = "dsh plugin: role-based model routing — planner and executor subagents on deepseek-v4.1-flash-expires-on-0910 (beta, expires 09-10)";
    homepage = "https://github.com/thedeveloper256/dsh-model-router";
    license = lib.licenses.mit;
  };
}
