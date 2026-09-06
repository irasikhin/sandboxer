# dsh-find-plugin — lets the dsh AGENT find plugins from the curated
# awesome-dsh-plugin registry by keyword/category. Vendored like dshmarket:
# copied as a REAL dir into dsh's node_modules (see dshmarket's header for
# why symlinks would break), peers stripped (cordis and dsh-tools come from
# the dsh installation's closure at runtime).
#
# Zero runtime dependencies, so no npm install at all: buildNpmPackage's
# install phase would fail on the absent node_modules. A plain unpack keeps
# exactly what the published tarball ships.
{
  lib,
  stdenv,
  fetchurl,
  jq,
}:

let
  version = "0.3.7";
  sourceHash = "sha256-RsQI/J9km5DsJbMPoC3MDx54PEomnoJGi3CVzhz/qZI=";
in
stdenv.mkDerivation {
  pname = "dsh-find-plugin";
  inherit version;

  src = fetchurl {
    url = "https://registry.npmjs.org/dsh-find-plugin/-/dsh-find-plugin-${version}.tgz";
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
    mkdir -p "$out/lib/node_modules"
    mv package "$out/lib/node_modules/dsh-find-plugin"
    runHook postInstall
  '';

  meta = {
    description = "find dsh plugins from the awesome-dsh-plugin curated registry without leaving the agent";
    homepage = "https://github.com/awesome-dsh-plugin/dsh-find-plugin";
    license = lib.licenses.mit;
  };
}
