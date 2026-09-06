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
# sourceHash: `nix store prefetch-file --hash-type sha256 <tarball url>` (the
# FLAT file hash — fetchurl fetches the tarball itself, not its unpacked NAR).
# npmDepsHash: take the `got:` line of the build's own hash-mismatch error —
# prefetch-npm-deps hashes the v1 cache layout, and this package (like pi's)
# pins npmDepsFetcherVersion = 2, whose layout hashes differently.
# The launcher owns the profile bootstrapping (web-bind overlay, baked
# plugins, heap cap) — a bump must re-check dsh-launch.sh, web-bind.patch.yml
# and dsh-profiles.json against the new release (templates, row schemas).
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
  # Baked community plugins (see assets/dsh-plugins/): copied INTO this
  # package's node_modules as real dirs, so any profile naming one in
  # dsh.profile.bundles resolves it from the installation. Optional — the
  # package builds standalone without them (plugins = {}).
  plugins ? { },
}:

let
  version = "0.1.2-rc.1";
  sourceHash = "sha256-yjcGaAU61tCsMl6RnvX2XeU94At7rXgAjm+0It/ONTA=";
  npmDepsHash = "sha256-jpiqNMOU76u5sYy59830bQzjFwJBwGnmTCavDT34IqE=";

  # Baked plugins by npm identity: the src dir under each derivation's
  # lib/node_modules that lands in this package's node_modules verbatim.
  dshmarket = plugins.dshmarket or null;
  dsh-find-plugin = plugins."dsh-find-plugin" or null;
  archify-dsh = plugins.archify-dsh or null;

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
  # npm >= 11.5 defaults to the allow-scripts policy, and nixpkgs' npm
  # config hook hardcodes `npm ci --ignore-scripts` (scripts run only in its
  # later `npm rebuild`, gated by the same policy) — so without this every
  # dependency install script is skipped: node-pty (the PTY behind the bash
  # tool; dsh-subprocess-local imports it at boot) and koffi would ship
  # without their native addons and every profile boot dies loading pty.node.
  # The flag satisfies the policy gate for the vendored, hashed lock (the old
  # npm ran every script anyway). node-pty's prebuild.js cannot download in
  # the nix sandbox, so its `|| node-gyp rebuild` fallback compiles against
  # the nodedir the build exports.
  npmRebuildFlags = [ "--dangerously-allow-all-scripts" ];
  # The npm tarball ships lib/ prebuilt — nothing to compile.
  dontNpmBuild = true;

  nativeBuildInputs = [
    makeWrapper
    python3 # node-gyp, for node-pty
  ];

  postInstall = ''
    # Baked plugins, copied (never symlinked) into this package's
    # node_modules: a symlink realpaths out of this tree and the plugins'
    # stripped peer imports (@deepseek-ai/cordis, schemastery, dsh-tools)
    # would resolve to nothing — copied, they walk up into this closure.
    mkdir -p "$out/lib/node_modules/@deepseek-ai/dsh/node_modules/@tt-a1i"
    ${lib.optionalString (dshmarket != null) ''
      cp -r ${dshmarket}/lib/node_modules/dshmarket "$out/lib/node_modules/@deepseek-ai/dsh/node_modules/"
    ''}
    ${lib.optionalString (dsh-find-plugin != null) ''
      cp -r ${dsh-find-plugin}/lib/node_modules/dsh-find-plugin "$out/lib/node_modules/@deepseek-ai/dsh/node_modules/"
    ''}
    ${lib.optionalString (archify-dsh != null) ''
      cp -r ${archify-dsh}/lib/node_modules/@tt-a1i/archify-dsh "$out/lib/node_modules/@deepseek-ai/dsh/node_modules/@tt-a1i/"
    ''}

    ${lib.optionalString stdenv.hostPlatform.isLinux ''
      # node-pty 1.2.x ships prebuilt addons for EVERY platform including
      # linux (prebuilds/linux-*/pty.node) — that one is what the runtime
      # loads, KEEP it. darwin/win32 prebuilds are dead weight in a Linux
      # image (most of it Windows DLLs).
      rm -rf "$out/lib/node_modules/@deepseek-ai/dsh/node_modules/node-pty/prebuilds/darwin-"* \
             "$out/lib/node_modules/@deepseek-ai/dsh/node_modules/node-pty/prebuilds/win32-"*
    ''}
    # dsh runs through ./dsh-launch.sh, which owns everything that has to be
    # in argv: --expose-internals (NO profile boots without it — see the
    # script), the baked-plugin profile bootstrapping, the web bind overlay
    # that makes a published port actually reach the UI, and the heap cap —
    # all of the last three inside a sandbox only. The wrapper around it
    # carries the environment.
    install -Dm644 ${./web-bind.patch.yml} "$out/share/sandboxer/dsh-web-bind.patch.yml"
    install -Dm644 ${./dsh-profiles.json} "$out/share/sandboxer/dsh-profiles.json"
    mkdir -p "$out/libexec"
    substitute ${./dsh-launch.sh} "$out/libexec/dsh-launch" \
      --subst-var-by node ${lib.getExe nodejs} \
      --subst-var-by bin "$out/lib/node_modules/@deepseek-ai/dsh/lib/bin.js" \
      --subst-var-by patch "$out/share/sandboxer/dsh-web-bind.patch.yml" \
      --subst-var-by profiles "$out/share/sandboxer/dsh-profiles.json" \
      --subst-var-by node_modules "$out/lib/node_modules/@deepseek-ai/dsh/node_modules"
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
      --prefix PATH : ${lib.makeBinPath [
        pnpm
        jq # the launcher merges baked plugin bundles into profiles
      ]} \
      --set-default DSH_TELEMETRY_DISABLED 1
  '';

  meta = {
    description = "DeepSeek Harness — the dsh agent-harness launcher (web UI and headless profiles)";
    homepage = "https://github.com/deepseek-ai/deepseek-harness";
    license = lib.licenses.mit;
    mainProgram = "dsh";
  };
}
