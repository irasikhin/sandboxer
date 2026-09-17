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
# DEEPSEEK V4.1 FLASH — no longer patched here. Up to 0.6.3 the published
# plugin routed the planner to deepseek-v4-pro and executors to
# deepseek-v4-flash, so the image rewrote both the router row and the model
# catalog onto V4.1 Flash. 0.7.0 ships that routing upstream: both roles and
# the `llm-deepseek` catalog row use `deepseek-flash` — the id
# api.deepseek.com actually serves (measured 2026-09-17: `deepseek-flash`
# HTTP 200, `deepseek-v4.1-flash` HTTP 400 "supported API model names are
# deepseek-flash, deepseek-v4-pro"). So there is nothing left to rewrite;
# the published bundle patch is installed as shipped. The old beta id and its
# 09-10 expiry are gone from this package entirely.
#
# COMPATIBILITY BRIDGE onto dsh >= 0.1.2 (see installPhase): the published
# plugin still targets the pre-0.1.2 harness API. The harness 0.1.2 rewrite
# removed the plugin's imported `foldPlanMode` (dsh-plan-mode) and
# `installSettingsSection`/`settingsNamespace` (dsh-settings) — verified still
# absent in 0.1.5-rc.2 — so the plugin cannot even load unpatched on the dsh
# release this image bakes. The bridge stubs exactly those three symbols;
# what remains:
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
  version = "0.7.0";
  sourceHash = "sha256-kVhb1FUqm8aDskJIf0WbqXIvQbxqD+M4nqRD2uoq8eo=";
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
    # The compatibility bridge — see the file header. The import lines are
    # unchanged in 0.7.0, so the --replace-fail guards still certify them.
    substituteInPlace package/lib/index.js \
      --replace-fail \
        'import { foldPlanMode } from "@deepseek-ai/dsh-plan-mode";' \
        'const foldPlanMode = () => false; // dsh >= 0.1.2: plan-mode projection rewritten; mode:"plan" degrades to strict' \
      --replace-fail \
        'import { installSettingsSection, settingsNamespace } from "@deepseek-ai/dsh-settings";' \
        'const settingsNamespace = (ns) => ns; const installSettingsSection = () => () => {}; // dsh >= 0.1.2: settings sections live in SettingsProvider'
    # 0.7.0 routes V4.1 Flash upstream (`deepseek-flash`) — no model patch.
    mkdir -p "$out/lib/node_modules"
    mv package "$out/lib/node_modules/dsh-model-router"
    runHook postInstall
  '';

  meta = {
    description = "dsh plugin: role-based model routing — planner and executor subagents on DeepSeek V4.1 Flash (deepseek-flash)";
    homepage = "https://github.com/thedeveloper256/dsh-model-router";
    license = lib.licenses.mit;
  };
}
