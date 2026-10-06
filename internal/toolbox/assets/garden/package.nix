{
  lib,
  stdenv,
  fetchurl,
  autoPatchelfHook,
}:

# Garden CLI (garden.io) — not in nixpkgs, so the official release binary is
# repackaged like microsandbox (nix/microsandbox.nix): the release is a generic
# glibc ELF that will not run on NixOS out of the box (stub-ld), so
# autoPatchelfHook patches the interpreter and the two non-glibc NEEDED
# libraries (libgcc_s).
#
# garden is a SELF-EXTRACTING launcher: at first run it unpacks its own Node
# runtime + JS bundle under the user's data dir and spawns that node directly.
# The unpacked node is a FOREIGN binary — its interpreter is the standard
# /lib64/ld-linux… path and it needs libstdc++ — neither of which a nix-built
# image provides. That is why the image also carries a loader-compat symlink
# and wraps this binary with LD_LIBRARY_PATH (see images.nix gardenCli /
# loaderCompat); this derivation only packages the launcher itself.
#
# Bumping = change version + both hashes (from the release's .sha256 files,
# hex→SRI via `nix hash convert --hash-algo sha256 --to sri <hex>`).
let
  version = "0.14.20";
  platforms = {
    "x86_64-linux" = {
      asset = "garden-${version}-linux-amd64.tar.gz";
      dir = "linux-amd64";
      hash = "sha256-g+FISR4Ay1yoMcrrQBo9G8XLjWa5chUK+WoSmreqxQU=";
    };
    "aarch64-linux" = {
      asset = "garden-${version}-linux-arm64.tar.gz";
      dir = "linux-arm64";
      hash = "sha256-ehlnl2JezQGxPHkICaj/0t58C2M2v/mQuNhbmNNC284=";
    };
  };
  plat =
    platforms.${stdenv.hostPlatform.system}
      or (throw "garden: unsupported system ${stdenv.hostPlatform.system} (linux x86_64/aarch64)");
in
stdenv.mkDerivation {
  pname = "garden";
  inherit version;

  src = fetchurl {
    url = "https://github.com/garden-io/garden/releases/download/${version}/${plat.asset}";
    inherit (plat) hash;
  };

  sourceRoot = ".";

  nativeBuildInputs = [ autoPatchelfHook ];
  # The release's only non-glibc NEEDED library (librt/libpthread/libdl/libc
  # all come from the glibc the patched interpreter resolves).
  buildInputs = [ stdenv.cc.cc.lib ];

  dontBuild = true;
  dontConfigure = true;

  installPhase = ''
    runHook preInstall
    mkdir -p $out/bin
    install -m755 ${plat.dir}/garden $out/bin/garden
    runHook postInstall
  '';

  meta = {
    description = "Garden CLI — Kubernetes and container development environments as code";
    homepage = "https://github.com/garden-io/garden";
    license = lib.licenses.mpl20;
    platforms = builtins.attrNames platforms;
    sourceProvenance = [ lib.sourceTypes.binaryNativeCode ];
    mainProgram = "garden";
  };
}
