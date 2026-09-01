{
  description = "cpak package manager";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      version = "2.11.0";
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
    in
    {
      packages = forAllSystems (system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in
        rec {
          cpak = pkgs.buildGoModule {
            pname = "cpak";
            inherit version;
            src = ./.;
            vendorHash = "sha256-5ywPXNyb2YF1Z9XQBHTTghPOMERVauKAm+8DxgZ7D5k=";

            nativeBuildInputs = [ pkgs.pkg-config ];
            buildInputs = [
              pkgs.gtk3
              pkgs.libadwaita
              pkgs.qt6.qtbase
            ];
            dontWrapQtApps = true;

            preBuild = ''
              make UI_ADAPTERS=all ui-adapters
            '';
            overrideModAttrs = final: previous: {
              preBuild = "";
            };

            subPackages = [
              "."
              "cmd/cpak-storaged"
            ];

            ldflags = [
              "-s"
              "-w"
              "-X=main.version=v${version}"
              "-X=main.selfUpdateMode=disabled"
              "-X=github.com/mirkobrombin/cpak/pkg/desktopui.defaultBackend=auto"
            ];

            postInstall = ''
              install -Dm644 pkg/systemauthority/assets/it.cpak.SystemAuthority1.conf \
                "$out/share/dbus-1/system.d/it.cpak.SystemAuthority1.conf"
              substituteInPlace "$out/share/dbus-1/system.d/it.cpak.SystemAuthority1.conf" \
                --replace-fail '@SERVICEDIR@' ""

              install -Dm644 pkg/systemauthority/assets/it.cpak.SystemAuthority1.service \
                "$out/share/dbus-1/system-services/it.cpak.SystemAuthority1.service"
              substituteInPlace "$out/share/dbus-1/system-services/it.cpak.SystemAuthority1.service" \
                --replace-fail '@BINARY@' "$out/bin/cpak"

              install -Dm644 pkg/systemauthority/assets/cpak-system-authority.service \
                "$out/lib/systemd/system/cpak-system-authority.service"
              substituteInPlace "$out/lib/systemd/system/cpak-system-authority.service" \
                --replace-fail '@BINARY@' "$out/bin/cpak"

              install -Dm644 pkg/systemauthority/assets/it.cpak.system.policy \
                "$out/share/polkit-1/actions/it.cpak.system.policy"
              install -Dm644 cpak-icon.png "$out/share/icons/hicolor/512x512/apps/it.cpak.cpak.png"
            '';

            meta = {
              description = "Rootless package manager with per-application sandboxes";
              homepage = "https://cpak.it";
              license = nixpkgs.lib.licenses.lgpl21Only;
              mainProgram = "cpak";
              platforms = nixpkgs.lib.platforms.linux;
            };
          };
          default = cpak;
        });

      nixosModules.default = { config, lib, pkgs, ... }:
        let
          cfg = config.services.cpak;
        in
        {
          options.services.cpak = {
            enable = lib.mkEnableOption "cpak system authority";
            package = lib.mkOption {
              type = lib.types.package;
              default = self.packages.${pkgs.system}.cpak;
              description = "The cpak package to use.";
            };
          };

          config = lib.mkIf cfg.enable {
            environment.systemPackages = [ cfg.package ];
            security.polkit.enable = true;
            services.dbus.packages = [ cfg.package ];
            systemd.packages = [ cfg.package ];
          };
        };

      checks = forAllSystems (system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in
        {
          package = self.packages.${system}.cpak;
        }
        // nixpkgs.lib.optionalAttrs (system == "x86_64-linux") {
          nixos-module =
            let
              sandbox-test = pkgs.buildGoModule {
                pname = "cpak-sandbox-test";
                inherit version;
                src = ./.;
                vendorHash = "sha256-5ywPXNyb2YF1Z9XQBHTTghPOMERVauKAm+8DxgZ7D5k=";
                doCheck = false;
                buildPhase = ''
                  runHook preBuild
                  go test -c -o cpak-sandbox-test ./pkg/sandbox
                  runHook postBuild
                '';
                installPhase = ''
                  runHook preInstall
                  install -Dm755 cpak-sandbox-test "$out/bin/cpak-sandbox-test"
                  runHook postInstall
                '';
              };
            in
            pkgs.testers.runNixOSTest {
              name = "cpak";
              nodes.machine = { ... }: {
                imports = [ self.nixosModules.default ];
                services.cpak.enable = true;
                environment.systemPackages = [ sandbox-test ];
                users.users.cpak-test = {
                  isNormalUser = true;
                  createHome = true;
                };
              };
              testScript = ''
                machine.wait_for_unit("multi-user.target")
                machine.succeed("cpak --version | grep -Fx v${version}")
                machine.succeed("cpak system status")
                machine.succeed("cpak system setup")
                machine.succeed("busctl --system call org.freedesktop.DBus /org/freedesktop/DBus org.freedesktop.DBus StartServiceByName su it.cpak.SystemAuthority1 0")
                machine.succeed("busctl --system call org.freedesktop.DBus /org/freedesktop/DBus org.freedesktop.DBus NameHasOwner s it.cpak.SystemAuthority1 | grep -q true")
                machine.fail("cpak system remove")
                machine.succeed("runuser -u cpak-test -- cpak-sandbox-test -test.run '^TestSeccompAllowsNestedUserNamespacesWhenRequested$' -test.v | tee /tmp/cpak-sandbox-test.log")
                machine.succeed("grep -F -- '--- PASS: TestSeccompAllowsNestedUserNamespacesWhenRequested' /tmp/cpak-sandbox-test.log")
                machine.fail("grep -F -- '--- SKIP:' /tmp/cpak-sandbox-test.log")
              '';
            };
        });
    };
}
