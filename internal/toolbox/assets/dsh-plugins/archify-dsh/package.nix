# @tt-a1i/archify-dsh — the Archify architecture-diagram Skill provider for
# dsh (the dsh half of tt-a1i/archify). Skill-only bundle: zero dependencies,
# zero peers, fully offline. Vendored like dshmarket: a REAL dir inside dsh's
# node_modules. Note the launcher additionally links baked plugins into each
# profile's node_modules, because archify's bundle patch resolves ITS OWN
# package via require(baseUrl) — a resolution only a profile-local
# (or pnpm-installed) copy satisfies.
#
# Zero runtime dependencies, so no npm install at all (buildNpmPackage's
# install phase would fail on the absent node_modules) — a plain unpack keeps
# exactly what the published tarball ships (the patch + Skill markdown).
{
  lib,
  stdenv,
  fetchurl,
  jq,
}:

let
  version = "0.1.0";
  sourceHash = "sha256-MZKv5UlJ2mSUrOo70r9RE6WOp4ZRETsSibzHISEw3zg=";
in
stdenv.mkDerivation {
  pname = "archify-dsh";
  inherit version;

  src = fetchurl {
    url = "https://registry.npmjs.org/@tt-a1i/archify-dsh/-/archify-dsh-${version}.tgz";
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
    jq 'del(.devDependencies, .peerDependencies)' package/package.json > package/package.json.stripped
    mv package/package.json.stripped package/package.json
    mkdir -p "$out/lib/node_modules/@tt-a1i"
    mv package "$out/lib/node_modules/@tt-a1i/archify-dsh"
    runHook postInstall
  '';

  meta = {
    description = "Archify architecture-diagram Skill provider for dsh";
    homepage = "https://github.com/tt-a1i/archify";
    license = lib.licenses.mit;
  };
}
