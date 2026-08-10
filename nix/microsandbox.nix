# microsandbox (`msb`) — the microVM runtime for `backend = "microsandbox"`,
# on libkrun (KVM on Linux, HVF on macOS); see docs/microsandbox-spike.md for
# the evaluation that selected it.
#
# Not in nixpkgs, and the upstream release is a generic dynamically-linked ELF
# that will not run on NixOS out of the box (stub-ld), so this repackages the
# official binary with autoPatchelfHook. Two things the packaging must get right:
#
#   - a REAL patchelf, never an `ld-linux --library-path … ./msb` shim: msb
#     re-execs itself as a `sandbox` child, and under a loader shim
#     /proc/self/exe is the loader, so the child dies with "error while loading
#     shared libraries: sandbox".
#   - libkrunfw must sit BESIDE the resolved binary under its exact versioned
#     name (msb resolves it from its own path, not from LD_LIBRARY_PATH). Hence
#     both land in libexec/ and bin/msb is a SYMLINK — a wrapper script would
#     resolve /proc/self/exe to the wrapper's target anyway, but a symlink keeps
#     the pair adjacent with nothing in between.
#
# Pin bump: change `version`, `libkrunfw` and every `hash` (from the release
# checksums.sha256, hex→SRI). A stale hash is a build-time mismatch, not a
# silent wrong binary. The project is pre-1.0 and its CLI still moves, so the
# pin is deliberate — gate an upgrade on the microsandbox integration suite
# (internal/backend/msb_real_integration_test.go).
{
  lib,
  stdenv,
  fetchurl,
  autoPatchelfHook,
  libcap_ng,
}:

let
  version = "0.6.7";
  # The versioned libkrunfw soname the release ships; msb dlopens exactly this
  # file name from beside itself.
  libkrunfw = "libkrunfw.so.5.6.0";
  platforms = {
    "x86_64-linux" = {
      asset = "microsandbox-linux-x86_64.tar.gz";
      hash = "sha256-XK4uHWe5dqZZuupz9CvYU2Edr5Lk+Bh1dNsGia0H6Cg=";
    };
    "aarch64-linux" = {
      asset = "microsandbox-linux-aarch64.tar.gz";
      hash = "sha256-dYq13oowsMufi9UJoNlGIqn+bHAWT/g4LhVfDPkogvo=";
    };
    "aarch64-darwin" = {
      asset = "microsandbox-darwin-aarch64.tar.gz";
      hash = "sha256-GZ2qWsB3lPVFQV0qMuSQR9L1Y8URVu1z6AwOkYRbodw=";
    };
  };
  plat =
    platforms.${stdenv.hostPlatform.system}
      or (throw "microsandbox: unsupported system ${stdenv.hostPlatform.system} (linux x86_64/aarch64, darwin aarch64)");
in
stdenv.mkDerivation {
  pname = "microsandbox";
  inherit version;

  src = fetchurl {
    url = "https://github.com/microsandbox/microsandbox/releases/download/v${version}/${plat.asset}";
    inherit (plat) hash;
  };

  sourceRoot = ".";

  nativeBuildInputs = lib.optionals stdenv.hostPlatform.isLinux [ autoPatchelfHook ];
  # msb's only non-glibc NEEDED library, plus libgcc_s.
  buildInputs = lib.optionals stdenv.hostPlatform.isLinux [
    libcap_ng
    stdenv.cc.cc.lib
  ];

  dontStrip = true;
  dontBuild = true;
  dontConfigure = true;

  installPhase = ''
    runHook preInstall
    mkdir -p $out/libexec/microsandbox $out/bin
    install -m755 msb $out/libexec/microsandbox/msb
    install -m755 ${libkrunfw} $out/libexec/microsandbox/${libkrunfw}
    ln -s $out/libexec/microsandbox/msb $out/bin/msb
    runHook postInstall
  '';

  meta = {
    description = "OCI-native microVM runtime (libkrun) — sandboxer's microsandbox backend";
    homepage = "https://microsandbox.dev";
    license = lib.licenses.asl20;
    platforms = builtins.attrNames platforms;
    sourceProvenance = [ lib.sourceTypes.binaryNativeCode ];
    mainProgram = "msb";
  };
}
