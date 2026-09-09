# dsh-model-router — role-based model routing for dsh: the planner (root)
# agent runs on deepseek-v4-pro, every delegated executor subagent on
# deepseek-v4-flash. Vendored like dshmarket: copied as a REAL dir into
# dsh's node_modules, peers stripped (cordis, schemastery — resolve at
# runtime from the dsh installation).
#
# Zero runtime dependencies, so no npm install at all: a plain unpack keeps
# exactly what the published tarball ships (lib/, the bundle patch, the
# pro-flash-routing skill).
#
# NOTE (2026-09-09): an earlier revision of this file patched the published
# defaults to route executors to deepseek-v4.1-flash, on the day the model was
# announced. The live DeepSeek API does NOT serve that id (verified: /models
# lists only v4-pro/v4-flash/v4-flash-vision-exp, and a chat completion with
# deepseek-v4.1-flash is rejected with HTTP 400). Routing to a model the API
# rejects breaks EVERY delegated request, so the patch was reverted and the
# plugin ships upstream's v4 defaults. When the API actually lists a new flash
# model, patch the executor route + llm-deepseek catalog here (with
# --replace-fail guards) or wait for upstream to adopt it — the build will not
# fail on our side either way unless we patch.
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
    mkdir -p "$out/lib/node_modules"
    mv package "$out/lib/node_modules/dsh-model-router"
    runHook postInstall
  '';

  meta = {
    description = "dsh plugin: role-based model routing — planner on deepseek-v4-pro, executor subagents on deepseek-v4-flash";
    homepage = "https://github.com/thedeveloper256/dsh-model-router";
    license = lib.licenses.mit;
  };
}
