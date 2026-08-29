{
  description = "Polis: a minimal lifecycle kernel for autonomous agents";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
      pkgsFor = system: import nixpkgs { inherit system; };
      polisFor =
        system:
        let
          pkgs = pkgsFor system;
        in
        pkgs.buildGoModule {
          pname = "polis";
          version = "0.1.0";
          src = self;
          vendorHash = "sha256-sZ2i3aCVufv5d2/NWb2OpM7/omEo1RmVmfOou+WyVKM=";
          subPackages = [ "./..." ];

          ldflags = [
            "-s"
            "-w"
          ];

          meta = {
            description = "Minimal lifecycle kernel for autonomous agents";
            homepage = "https://github.com/adamtopaz/polis";
            mainProgram = "polis";
          };
        };
    in
    {
      packages = forAllSystems (
        system:
        let
          pkgs = pkgsFor system;
          polis = polisFor system;
        in
        {
          default = polis;
          container = pkgs.dockerTools.buildLayeredImage {
            name = "polis";
            tag = "dev";
            contents = [
              polis
              pkgs.cacert
              pkgs.tini
            ];
            config = {
              Cmd = [
                "/bin/tini"
                "--"
                "/bin/polis"
                "server"
              ];
              Env = [
                "PATH=/bin"
                "POLIS_DB_PATH=/data/polis.db"
              ];
              ExposedPorts."8080/tcp" = { };
              Labels = {
                "org.opencontainers.image.description" = "Polis autonomous-agent lifecycle kernel";
                "org.opencontainers.image.revision" =
                  if self ? rev then
                    self.rev
                  else if self ? dirtyRev then
                    self.dirtyRev
                  else
                    "dirty";
                "org.opencontainers.image.source" = "https://github.com/adamtopaz/polis";
              };
              User = "10001:10001";
              WorkingDir = "/";
            };
          };
        }
      );

      checks = forAllSystems (
        system:
        let
          pkgs = pkgsFor system;
        in
        {
          package = polisFor system;
          formatting = pkgs.runCommand "polis-formatting" { nativeBuildInputs = [ pkgs.go ]; } ''
            cd ${self}
            unformatted="$(gofmt -l .)"
            if [ -n "$unformatted" ]; then
              echo "$unformatted"
              exit 1
            fi
            touch $out
          '';
          workflows = pkgs.runCommand "polis-workflows" { nativeBuildInputs = [ pkgs.actionlint ]; } ''
            cd ${self}
            actionlint .github/workflows/*.yml
            touch $out
          '';
        }
      );

      apps = forAllSystems (
        system:
        let
          polis = polisFor system;
        in
        {
          default = {
            type = "app";
            program = "${polis}/bin/polis";
          };
        }
      );

      devShells = forAllSystems (
        system:
        let
          pkgs = pkgsFor system;
        in
        {
          default = pkgs.mkShell {
            packages = [
              pkgs.curl
              pkgs.go
              pkgs.gopls
              pkgs.gotools
              pkgs.jq
            ];
          };
        }
      );

      formatter = forAllSystems (system: (pkgsFor system).nixfmt);
    };
}
