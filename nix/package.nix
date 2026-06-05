{
  lib,
  buildGoModule,
  makeWrapper,
  git,
  rsync,
  bubblewrap,
  coreutils,
  gnused,
  gnugrep,
  gawk,
}:

let
  pname = "sandboxer";
  version = "0.9.1";
in
buildGoModule {
  inherit pname version;

  src = lib.cleanSource ./..;

  vendorHash = "sha256-komX1AmHt2NoF1x6xsNa2RFkfVzOXfYEMPhT0zwMxjw=";

  subPackages = [ "cmd/sandboxer" ];

  ldflags = [
    "-s"
    "-w"
    "-X"
    "main.version=v${version}"
  ];

  doCheck = true;

  nativeBuildInputs = [ makeWrapper ];

  # Runtime deps kept on PATH via --prefix (host PATH is preserved too, so a
  # host-installed `claude` and podman/docker stay reachable). No node/gost: the
  # config is native Go and the egress proxy is the binary itself (_proxy mode).
  postInstall = ''
    wrapProgram $out/bin/sandboxer \
      --prefix PATH : ${
        lib.makeBinPath [
          git
          rsync
          bubblewrap
          coreutils
          gnused
          gnugrep
          gawk
        ]
      }

    mkdir -p $out/share/bash-completion/completions
    $out/bin/sandboxer completion bash > $out/share/bash-completion/completions/sandboxer

    mkdir -p $out/share/zsh/site-functions
    $out/bin/sandboxer completion zsh > $out/share/zsh/site-functions/_sandboxer

    mkdir -p $out/share/fish/vendor_completions.d
    $out/bin/sandboxer completion fish > $out/share/fish/vendor_completions.d/sandboxer.fish
  '';

  meta = {
    description = "Config-driven, multi-agent, containerized dev sandboxes";
    homepage = "https://github.com/irasikhin/sandboxer";
    license = lib.licenses.mit;
    mainProgram = "sandboxer";
    platforms = lib.platforms.linux;
  };
}
