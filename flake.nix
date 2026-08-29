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
      piRuntimeFor =
        system:
        let
          pkgs = pkgsFor system;
        in
        pkgs.buildNpmPackage {
          pname = "polis-pi-runtime";
          version = "0.1.0";
          src = self + /runtime/pi;
          nodejs = pkgs.nodejs_22;
          npmDepsFetcherVersion = 2;
          npmDepsHash = "sha256-t6EVHb9TX/vJfM3A6BSgKJP+8iBQHQBL+vGNK5JRawc=";

          nativeBuildInputs = [ pkgs.makeWrapper ];
          npmBuildScript = "build";
          doCheck = true;
          checkPhase = ''
            runHook preCheck
            node --test dist/test/*.test.js
            runHook postCheck
          '';

          installPhase = ''
            runHook preInstall
            mkdir -p $out/lib/polis-pi-runtime $out/bin
            cp -r dist node_modules package.json $out/lib/polis-pi-runtime/
            makeWrapper ${pkgs.nodejs_22}/bin/node $out/bin/polis-pi-agent \
              --add-flags "$out/lib/polis-pi-runtime/dist/src/main.js"
            runHook postInstall
          '';

          meta = {
            description = "Persistent Pi SDK runtime for Polis agents";
            homepage = "https://github.com/adamtopaz/polis";
            mainProgram = "polis-pi-agent";
          };
        };
    in
    {
      packages = forAllSystems (
        system:
        let
          pkgs = pkgsFor system;
          polis = polisFor system;
          piRuntime = piRuntimeFor system;
        in
        {
          default = polis;
          pi-runtime = piRuntime;
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
          pi-container = pkgs.dockerTools.buildLayeredImage {
            name = "polis-pi";
            tag = "dev";
            contents = [
              polis
              piRuntime
              pkgs.bashInteractive
              pkgs.cacert
              pkgs.coreutils
              pkgs.findutils
              pkgs.gitMinimal
              pkgs.gnugrep
              pkgs.gnused
              pkgs.nodejs_22
              pkgs.ripgrep
              pkgs.tini
            ];
            config = {
              Cmd = [
                "/bin/tini"
                "--"
                "/bin/polis"
                "worker"
              ];
              Env = [
                "NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-bundle.crt"
                "PATH=/bin"
                "POLIS_WORKSPACE_ROOT=/workspaces"
                "SSL_CERT_FILE=/etc/ssl/certs/ca-bundle.crt"
              ];
              Labels = {
                "org.opencontainers.image.description" = "Polis worker with the Pi SDK agent runtime";
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
              WorkingDir = "/workspaces";
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
          pi-runtime = piRuntimeFor system;
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
              pkgs.nodejs_22
            ];
          };
        }
      );

      formatter = forAllSystems (system: (pkgsFor system).nixfmt);
    };
}
