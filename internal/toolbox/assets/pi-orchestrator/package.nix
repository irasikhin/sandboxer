# pi-agent-orchestrator — the multi-agent orchestration package for pi
# (subagents, swarms, worktree isolation, the /agents dashboard), baked into
# the toolbox image and registered in every sandbox's pi settings by default
# (sandbox.EnsurePiPackages).
#
# Not in nixpkgs, so it is built here from the published npm tarball, the same
# way ./pi/package.nix builds pi itself. Two deliberate differences:
#
#   - dist/ ships prebuilt, so the build only needs the RUNTIME deps. The
#     package's devDependencies pull in vitest/biome/typescript AND a second
#     copy of pi (357 of the lock's 368 entries); its peerDependencies name
#     pi packages that pi supplies to extensions itself and that a package
#     must NOT bundle (upstream docs/packages.md). Both are stripped from
#     package.json in the source prep, and the vendored lock is generated
#     from that same stripped package.json — 11 runtime packages.
#   - no bin: pi loads this as a package (extensions/skills/prompts declared
#     under the `pi` key in package.json), it is never executed directly.
#
# Bumping = update version + sourceHash + npmDepsHash + package-lock.json
# together. The lock is regenerated from the published tarball:
#   tar -xzf <tarball> -C src --strip-components=1
#   jq 'del(.devDependencies) | del(.peerDependencies)' src/package.json | sponge src/package.json
#   (cd src && npm install --package-lock-only)
# hashes: `nix hash file --sri --type sha256 <tarball>` and
# `nix run nixpkgs#prefetch-npm-deps -- src/package-lock.json`.
{
  lib,
  buildNpmPackage,
  fetchurl,
  runCommand,
  jq,
}:

let
  version = "0.18.0";
  sourceHash = "sha256-AI21CwdpEtZ7mhw0WKx3Ym2GbwLFYm7CrTOF+qRRcbg=";
  npmDepsHash = "sha256-yTfiU6fSbof/Dz7Ye6+rgEwETdMzmmCuOEosRYjOTjw=";

  # The published tarball, with dev/peer deps stripped and the vendored lock
  # placed beside it (see the header for why both are needed).
  srcWithLock =
    runCommand "pi-agent-orchestrator-src-${version}"
      {
        nativeBuildInputs = [ jq ];
      }
      ''
        mkdir -p $out
        tar -xzf ${
          fetchurl {
            url = "https://registry.npmjs.org/@groeponline/pi-agent-orchestrator/-/pi-agent-orchestrator-${version}.tgz";
            hash = sourceHash;
          }
        } -C $out --strip-components=1
        jq 'del(.devDependencies) | del(.peerDependencies)' $out/package.json > $out/package.json.stripped
        mv $out/package.json.stripped $out/package.json
        cp ${./package-lock.json} $out/package-lock.json
      '';
in
buildNpmPackage {
  pname = "pi-agent-orchestrator";
  inherit version;
  src = srcWithLock;

  npmDepsFetcherVersion = 2;
  inherit npmDepsHash;
  makeCacheWritable = true;
  # The npm tarball ships dist/ prebuilt — nothing to compile.
  dontNpmBuild = true;

  meta = {
    description = "Multi-agent orchestration package for the pi coding agent";
    homepage = "https://github.com/GroepOnline/pi-agent-orchestrator";
    license = lib.licenses.mit;
  };
}
