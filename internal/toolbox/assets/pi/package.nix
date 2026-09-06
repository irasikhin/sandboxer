# pi — vendored packaging. The other agents come PREBUILT from nixpkgs
# (cache.nixos.org); pi alone is not in nixpkgs, so it is built here from the
# published npm tarball. Node entry point, not the bun-compiled binary: the
# toolbox image already ships nodejs, and the node mode is upstream-supported.
# The npm tarball ships no usable lockfile, so package-lock.json is vendored
# beside this file. Bumping pi = update version + sourceHash + npmDepsHash +
# package-lock.json together (lock from upstream's repo at the release tag, or
# `npm i --package-lock-only` against the published tarball; hashes via nix's
# expected/actual mismatch error). Adapted from numtide/llm-agents.nix (MIT),
# whose binary cache being unreachable is why the input was dropped.
{
  lib,
  buildNpmPackage,
  fetchurl,
  runCommand,
  makeWrapper,
  fd,
  ripgrep,
}:

let
  version = "0.84.1";
  sourceHash = "sha256-ppoYWWAX6RlV/Q/Wd75p+rW26gHVsGIHvO407hUivCA=";
  npmDepsHash = "sha256-vx53B2ZhZ4/KuPU44r4vuepyGP6gpm9f1JOyHLBDdzE=";

  # The published tarball with the vendored lockfile placed beside it; the
  # shipped npm-shrinkwrap.json is dropped (it would shadow the lock).
  srcWithLock = runCommand "pi-src-with-lock" { } ''
    mkdir -p $out
    tar -xzf ${
      fetchurl {
        url = "https://registry.npmjs.org/@earendil-works/pi-coding-agent/-/pi-coding-agent-${version}.tgz";
        hash = sourceHash;
      }
    } -C $out --strip-components=1
    rm -f $out/npm-shrinkwrap.json
    cp ${./package-lock.json} $out/package-lock.json
  '';
in
buildNpmPackage {
  pname = "pi";
  inherit version;
  src = srcWithLock;

  npmDepsFetcherVersion = 2;
  inherit npmDepsHash;
  makeCacheWritable = true;
  # npm >= 11.5 defaults to the allow-scripts policy, and nixpkgs' npm
  # config hook hardcodes `npm ci --ignore-scripts` (scripts run only in its
  # later `npm rebuild`, gated by the same policy) — together they would skip
  # every dependency install script (@google/genai, protobufjs, fsevents
  # here). The flag restores the old npm behavior for the vendored, hashed
  # lock; see dsh/package.nix for the details.
  npmRebuildFlags = [ "--dangerously-allow-all-scripts" ];
  # The npm tarball ships dist/ prebuilt — nothing to compile.
  dontNpmBuild = true;

  nativeBuildInputs = [ makeWrapper ];

  postInstall = ''
    wrapProgram "$out/bin/pi" \
      --prefix PATH : ${
        lib.makeBinPath [
          fd
          ripgrep
        ]
      } \
      --set PI_SKIP_VERSION_CHECK 1 \
      --set PI_TELEMETRY 0
  '';

  meta = {
    description = "pi coding agent (node entry point)";
    homepage = "https://github.com/badlogic/pi-mono";
    license = lib.licenses.mit;
    mainProgram = "pi";
  };
}
