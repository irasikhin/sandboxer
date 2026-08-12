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
  version = "0.78.1";
in
buildGoModule {
  inherit pname version;

  src = lib.cleanSource ./..;

  vendorHash = "sha256-faYKtXCfCn08HEPTw5trZs+eCeDyzvhy7UeHhSJGRUo=";

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
  # egress proxy is a squid sidecar image, never the binary itself.
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

    # `sb` — a short alias for `sandboxer`. The binary is functionally identical
    # under either name (the command name only shapes help text), so a symlink to
    # the wrapped program is enough; it inherits the same runtime-PATH prefix.
    ln -s sandboxer $out/bin/sb

    # Completions for `sb`, reusing sandboxer's completion function (cobra's
    # completion resolves the invoked command name at runtime, so the same
    # function serves both — only the REGISTRATION is retargeted). Anchored
    # substitutions keep other "sandboxer" text untouched.
    sed -E 's/(complete .*-F __start_sandboxer) sandboxer$/\1 sb/' \
      $out/share/bash-completion/completions/sandboxer \
      > $out/share/bash-completion/completions/sb
    sed 's/-c sandboxer/-c sb/g' \
      $out/share/fish/vendor_completions.d/sandboxer.fish \
      > $out/share/fish/vendor_completions.d/sb.fish
    printf '#compdef sb\n_sandboxer "$@"\n' > $out/share/zsh/site-functions/_sb
  '';

  meta = {
    description = "Config-driven, multi-agent, containerized dev sandboxes";
    homepage = "https://github.com/irasikhin/sandboxer";
    license = lib.licenses.mit;
    mainProgram = "sandboxer";
    platforms = lib.platforms.linux;
  };
}
