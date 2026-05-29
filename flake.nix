{
  description = "sandboxer — параллельные автономные Claude-агенты, каждый в изолированной копии проекта (нативный /sandbox)";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      # Linux-only: нативный sandbox Claude Code на Linux использует bubblewrap.
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAll = f: nixpkgs.lib.genAttrs systems (system: f system nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAll (system: pkgs:
        let
          # Зависимости, кладущиеся в PATH обёртки. claude и systemd НЕ бандлятся —
          # claude проприетарен и ставится отдельно; systemd берётся от хоста.
          runtimeDeps = with pkgs; [
            bash coreutils gawk gnused gnugrep
            rsync git nodejs_22 bubblewrap socat
          ];
          sandboxer = pkgs.stdenvNoCC.mkDerivation {
            pname = "sandboxer";
            version = "0.1.0";
            src = ./.;
            nativeBuildInputs = [ pkgs.makeWrapper ];
            dontBuild = true;
            installPhase = ''
              runHook preInstall
              install -Dm755 bin/sandboxer "$out/bin/sandboxer"
              install -Dm644 sandboxer.tasks.example "$out/share/sandboxer/sandboxer.tasks.example"
              # --prefix: добавляем зависимости, НО сохраняем PATH хоста, чтобы
              # `claude` и `systemd-run` оставались доступны.
              wrapProgram "$out/bin/sandboxer" \
                --prefix PATH : ${pkgs.lib.makeBinPath runtimeDeps}
              runHook postInstall
            '';
            meta = with pkgs.lib; {
              description = "Параллельные автономные Claude-агенты в изолированных копиях проекта";
              mainProgram = "sandboxer";
              platforms = platforms.linux;
              license = licenses.mit;
            };
          };
        in {
          default = sandboxer;
          sandboxer = sandboxer;
        });

      apps = forAll (system: pkgs: {
        default = {
          type = "app";
          program = "${self.packages.${system}.default}/bin/sandboxer";
        };
      });

      devShells = forAll (system: pkgs: {
        default = pkgs.mkShell {
          packages = [ self.packages.${system}.default pkgs.nodejs_22 ];
          shellHook = ''
            echo "sandboxer devShell — нужен установленный 'claude' в PATH."
            echo "пример задач: share/sandboxer/sandboxer.tasks.example"
          '';
        };
      });
    };
}
