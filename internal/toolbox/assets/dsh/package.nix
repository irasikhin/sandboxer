# dsh — DeepSeek Harness, vendored packaging. Like pi (./../pi/package.nix),
# nixpkgs does not carry it, so it is built here from the published npm tarball
# and grafted into pkgs by the overlay in BOTH flakes (root + embedded).
#
# The published tarball ships lib/ prebuilt, so only the RUNTIME closure is
# needed: devDependencies (the repo's own test/build packages, a second copy of
# the agent among them) are stripped in the source prep, and the vendored lock
# is generated from that same stripped package.json.
#
# Native deps do get compiled: node-pty (the PTY behind the bash tool) and
# koffi run install scripts, so python3 is a build input. The rest of the
# platform-specific closure — @vscode/ripgrep (the grep/glob tools spawn the
# packaged binary, never a host rg), @deepseek-ai/node-addon-landlock-run
# (the Linux sandbox seam) and sharp — ships prebuilt per platform and is
# selected by npm from the optional deps, so both image architectures resolve
# from the one lock.
#
# Bumping = update version + sourceHash + npmDepsHash + package-lock.json
# together. The lock is regenerated from the published tarball:
#   tar -xzf <tarball> -C src --strip-components=1
#   jq 'del(.devDependencies)' src/package.json | sponge src/package.json
#   (cd src && npm install --package-lock-only --omit=dev)
# sourceHash: `nix store prefetch-file --hash-type sha256 <tarball url>`.
# npmDepsHash: take the `got:` line of the build's own hash-mismatch error —
# prefetch-npm-deps hashes the v1 cache layout, and this package (like pi's)
# pins npmDepsFetcherVersion = 2, whose layout hashes differently.
{
  lib,
  stdenv,
  buildNpmPackage,
  fetchurl,
  runCommand,
  makeWrapper,
  jq,
  nodejs,
  python3,
  pnpm,
  runtimeShell,
}:

let
  version = "0.1.0-rc.6";
  sourceHash = "sha256-G4qaCtPH/q7OR5JuC9N8oVHHzPqZeVOvpf0BJheE6tw=";
  npmDepsHash = "sha256-iPHX31lH4JmaKGpLOC29GXj1TMTksdEgzpzTMpoLXQI=";

  # The published tarball, with dev deps stripped and the vendored lock placed
  # beside it (see the header for why both are needed).
  srcWithLock =
    runCommand "dsh-src-${version}"
      {
        nativeBuildInputs = [ jq ];
      }
      ''
        mkdir -p $out
        tar -xzf ${
          fetchurl {
            url = "https://registry.npmjs.org/@deepseek-ai/dsh/-/dsh-${version}.tgz";
            hash = sourceHash;
          }
        } -C $out --strip-components=1
        jq 'del(.devDependencies)' $out/package.json > $out/package.json.stripped
        mv $out/package.json.stripped $out/package.json
        cp ${./package-lock.json} $out/package-lock.json
      '';
in
buildNpmPackage {
  pname = "dsh";
  inherit version;
  src = srcWithLock;

  npmDepsFetcherVersion = 2;
  inherit npmDepsHash;
  makeCacheWritable = true;
  # The npm tarball ships lib/ prebuilt — nothing to compile.
  dontNpmBuild = true;

  nativeBuildInputs = [
    makeWrapper
    python3 # node-gyp, for node-pty
  ];

  postInstall = ''
    ${lib.optionalString stdenv.hostPlatform.isLinux ''
      # node-pty ships prebuilt addons for darwin/win32 only (58M, most of it
      # Windows) and loads the one we just compiled into build/Release on
      # Linux — pure dead weight in a Linux image.
      rm -rf "$out/lib/node_modules/@deepseek-ai/dsh/node_modules/node-pty/prebuilds"
    ''}
    # dsh runs through ./dsh-launch.sh, which owns the two things that have to
    # be in argv: --expose-internals (NO profile boots without it — see the
    # script) and, inside a sandbox only, the web bind overlay that makes a
    # published port actually reach the UI. The wrapper around it carries the
    # environment.
    install -Dm644 ${./web-bind.patch.yml} "$out/share/sandboxer/dsh-web-bind.patch.yml"
    mkdir -p "$out/libexec"
    substitute ${./dsh-launch.sh} "$out/libexec/dsh-launch" \
      --subst-var-by node ${lib.getExe nodejs} \
      --subst-var-by bin "$out/lib/node_modules/@deepseek-ai/dsh/lib/bin.js" \
      --subst-var-by patch "$out/share/sandboxer/dsh-web-bind.patch.yml"
    # The interpreter is pinned HERE, not left to patchShebangs: buildNpmPackage's
    # fixup does not reach this file, and the toolbox image is a nix userland with
    # no /usr/bin/env at all — an unpatched `#!/usr/bin/env bash` makes every dsh
    # invocation inside a sandbox die with "bad interpreter". --replace-fail so a
    # reworded shebang breaks the build instead of shipping that again.
    substituteInPlace "$out/libexec/dsh-launch" \
      --replace-fail '#!/usr/bin/env bash' '#!${runtimeShell}'
    chmod +x "$out/libexec/dsh-launch"
    rm "$out/bin/dsh"
    makeWrapper "$out/libexec/dsh-launch" "$out/bin/dsh" \
      --prefix PATH : ${lib.makeBinPath [ pnpm ]} \
      --set-default DSH_TELEMETRY_DISABLED 1
  '';

  meta = {
    description = "DeepSeek Harness — the dsh agent-harness launcher (web UI and headless profiles)";
    homepage = "https://github.com/deepseek-ai/deepseek-harness";
    license = lib.licenses.mit;
    mainProgram = "dsh";
  };
}
