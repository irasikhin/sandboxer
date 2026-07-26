# smolvm — the microVM runtime the `backend = "microvm"` backend shells out to
# (libkrun: KVM on Linux). NOT in nixpkgs, and the upstream release is a generic
# dynamically-linked ELF that will not run on NixOS out of the box (stub-ld), so
# this repackages the official binary with autoPatchelfHook: the loader and
# libgcc are patched to the nix store, and the bundled libkrun/libkrunfw stay
# beside the binary on LD_LIBRARY_PATH. The bash wrapper the tarball ships is
# bypassed (it needs /usr/bin/env bash) — makeWrapper drives smolvm-bin directly.
#
# Pin bump: change `version` and both `hash`es (from the release
# checksums.sha256, hex→SRI). A stale hash is a build-time mismatch, not a silent
# wrong binary.
{
  lib,
  stdenv,
  fetchurl,
  autoPatchelfHook,
  makeWrapper,
}:

let
  version = "1.6.13";
  platforms = {
    "x86_64-linux" = {
      arch = "x86_64";
      hash = "sha256-nm/sNSVCZP3RXEuSfI7pcQTL7iAZVtP2kDpLjEw5OVY=";
    };
    "aarch64-linux" = {
      arch = "arm64";
      hash = "sha256-1SygeONEHv2Ne6aftOU2G0ThhhD7b7XVMBkCq4XAGTw=";
    };
  };
  plat =
    platforms.${stdenv.hostPlatform.system}
      or (throw "smolvm: unsupported system ${stdenv.hostPlatform.system} (linux x86_64/arm64 only)");
in
stdenv.mkDerivation {
  pname = "smolvm";
  inherit version;

  src = fetchurl {
    url = "https://github.com/smol-machines/smolvm/releases/download/v${version}/smolvm-${version}-linux-${plat.arch}.tar.gz";
    inherit (plat) hash;
  };

  nativeBuildInputs = [
    autoPatchelfHook
    makeWrapper
  ];
  buildInputs = [ stdenv.cc.cc.lib ]; # libgcc_s; libkrun is dlopen'd from the bundle

  dontStrip = true;

  # Patch ONLY the host-side binary and libkrun, never the whole output:
  # agent-rootfs/ is the GUEST filesystem, and rewriting its busybox/tar ELF
  # interpreters to the host nix store breaks them inside the VM (they'd look for
  # a /nix/store glibc that does not exist in the guest — the machine boots but
  # `tar`/init fail).
  dontAutoPatchelf = true;

  installPhase = ''
    runHook preInstall
    mkdir -p $out/libexec/smolvm $out/bin
    # --sparse=always keeps the ~10/20 GiB-apparent ext4 disk templates sparse
    # (a few hundred KiB on disk); expanding them would explode the store path.
    cp -a --sparse=always . $out/libexec/smolvm/
    makeWrapper $out/libexec/smolvm/smolvm-bin $out/bin/smolvm \
      --prefix LD_LIBRARY_PATH : $out/libexec/smolvm/lib \
      --set-default SMOLVM_AGENT_ROOTFS $out/libexec/smolvm/agent-rootfs
    runHook postInstall
  '';

  postFixup = ''
    autoPatchelf $out/libexec/smolvm/smolvm-bin $out/libexec/smolvm/lib
  '';

  meta = {
    description = "OCI-native microVM runtime (libkrun) — sandboxer's microvm backend";
    homepage = "https://smolmachines.com";
    license = lib.licenses.asl20;
    platforms = builtins.attrNames platforms;
    sourceProvenance = [ lib.sourceTypes.binaryNativeCode ];
    mainProgram = "smolvm";
  };
}
