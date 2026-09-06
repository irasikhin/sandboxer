# dshmarket — the dsh plugin market (browse/search/install from Settings),
# vendored like dsh itself. Baked INTO the dsh package's node_modules (real
# dirs, never symlinks — a symlinked package realpaths out of dsh's tree and
# its peer imports of @deepseek-ai/cordis and schemastery would resolve to
# nothing), so any profile naming it in dsh.profile.bundles resolves it from
# the dsh installation without a pnpm install.
#
# peerDependencies are stripped in the src prep and resolved at runtime from
# the dsh installation's own closure (cordis/schemastery/dsh-settings, all
# present in dsh 0.1.2-rc.1) — npm auto-installing them here would bake a
# SECOND copy of each and break the plugin-registry singleton. scripts is
# stripped too: the published tarball ships lib/ and client/ prebuilt, and
# the package's `prepare` would otherwise try `npm run build` during install
# against devDependencies that no longer exist.
#
# Bumping = update version + sourceHash + npmDepsHash + package-lock.json
# together (lock regenerated from the published tarball with devDeps, peers
# and scripts stripped; hashes like dsh's — see its header).
{
  lib,
  buildNpmPackage,
  fetchurl,
  runCommand,
  jq,
}:

let
  version = "1.44.0";
  sourceHash = "sha256-kV1xKsIiCaH3USz6Ou0DlkCO/LAJe3MBo12z4gyiNxg=";
  npmDepsHash = "sha256-ppnJXzWxrKicoW/qYf+jOyVifIn2sUcd12WLGew6Heo=";

  srcWithLock = runCommand "dshmarket-src-${version}" { nativeBuildInputs = [ jq ]; } ''
    mkdir -p $out
    tar -xzf ${
      fetchurl {
        url = "https://registry.npmjs.org/dshmarket/-/dshmarket-${version}.tgz";
        hash = sourceHash;
      }
    } -C $out --strip-components=1
    jq 'del(.devDependencies, .peerDependencies, .scripts)' $out/package.json > $out/package.json.stripped
    mv $out/package.json.stripped $out/package.json
    cp ${./package-lock.json} $out/package-lock.json
  '';
in
buildNpmPackage {
  pname = "dshmarket";
  inherit version;
  src = srcWithLock;

  npmDepsFetcherVersion = 2;
  inherit npmDepsHash;
  makeCacheWritable = true;
  # The npm tarball ships lib/ and client/ prebuilt — nothing to compile.
  dontNpmBuild = true;

  meta = {
    description = "dsh plugin market — browse, search and install community plugins from the dsh web UI";
    homepage = "https://github.com/dsh-market/dsh-market";
    license = lib.licenses.mit;
  };
}
