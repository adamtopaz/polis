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
      goProgramsFor =
        system:
        let
          pkgs = pkgsFor system;
        in
        pkgs.buildGoModule {
          pname = "polis-programs";
          version = "0.1.0";
          src = self;
          vendorHash = "sha256-sZ2i3aCVufv5d2/NWb2OpM7/omEo1RmVmfOou+WyVKM=";
          subPackages = [
            "./cmd/polis"
            "./cmd/polisctl"
            "./cmd/polis-controller"
            "./cmd/polis-worker"
            "./cmd/polis-demo-agent"
          ];

          ldflags = [
            "-s"
            "-w"
          ];

          checkPhase = ''
            runHook preCheck
            go test ./...
            runHook postCheck
          '';

          meta = {
            description = "Polis agent, operator, and infrastructure programs";
            homepage = "https://github.com/adamtopaz/polis";
            mainProgram = "polisctl";
          };
        };
      programFor =
        system: name: description:
        let
          pkgs = pkgsFor system;
        in
        pkgs.runCommand "${name}-0.1.0"
          {
            meta = {
              inherit description;
              homepage = "https://github.com/adamtopaz/polis";
              mainProgram = name;
            };
          }
          ''
            mkdir -p $out/bin
            cp ${goProgramsFor system}/bin/${name} $out/bin/${name}
          '';
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
          programs = goProgramsFor system;
          polis = programFor system "polis" "Agent capability CLI for Polis";
          polisctl = programFor system "polisctl" "Operator control CLI for Polis";
          controller = programFor system "polis-controller" "Polis controller";
          worker = programFor system "polis-worker" "Polis worker";
          demoAgent = programFor system "polis-demo-agent" "Deterministic Polis test agent";
          piRuntime = piRuntimeFor system;
          workerContainer =
            {
              name,
              description,
              runtimeContents,
              extraEnv ? [ ],
            }:
            pkgs.dockerTools.buildLayeredImage {
              inherit name;
              tag = "dev";
              contents = [
                polis
                worker
                pkgs.bashInteractive
                pkgs.cacert
                pkgs.coreutils
                pkgs.tini
              ]
              ++ runtimeContents;
              config = {
                Cmd = [
                  "/bin/tini"
                  "--"
                  "/bin/polis-worker"
                ];
                Env = [
                  "PATH=/bin"
                  "POLIS_WORKSPACE=/workspace"
                  "SSL_CERT_FILE=/etc/ssl/certs/ca-bundle.crt"
                ]
                ++ extraEnv;
                Labels = {
                  "org.opencontainers.image.description" = description;
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
        in
        {
          default = programs;
          inherit
            polis
            polisctl
            controller
            worker
            ;
          demo-agent = demoAgent;
          pi-runtime = piRuntime;
          container = pkgs.dockerTools.buildLayeredImage {
            name = "polis";
            tag = "dev";
            contents = [
              controller
              polisctl
              pkgs.cacert
              pkgs.tini
            ];
            config = {
              Cmd = [
                "/bin/tini"
                "--"
                "/bin/polis-controller"
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
          pi-container = workerContainer {
            name = "polis-pi";
            description = "Production Polis worker with the Pi SDK agent runtime";
            runtimeContents = [
              piRuntime
              pkgs.findutils
              pkgs.gitMinimal
              pkgs.gnugrep
              pkgs.gnused
              pkgs.nodejs_22
              pkgs.ripgrep
            ];
            extraEnv = [ "NODE_EXTRA_CA_CERTS=/etc/ssl/certs/ca-bundle.crt" ];
          };
          demo-container = workerContainer {
            name = "polis-demo";
            description = "Polis worker with the deterministic demo runtime";
            runtimeContents = [ demoAgent ];
          };
        }
      );

      checks = forAllSystems (
        system:
        let
          pkgs = pkgsFor system;
        in
        {
          package = goProgramsFor system;
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
          polis = programFor system "polis" "Agent capability CLI for Polis";
          polisctl = programFor system "polisctl" "Operator control CLI for Polis";
          controller = programFor system "polis-controller" "Polis controller";
          worker = programFor system "polis-worker" "Polis worker";
          demoAgent = programFor system "polis-demo-agent" "Deterministic Polis test agent";
        in
        {
          default = {
            type = "app";
            program = "${polisctl}/bin/polisctl";
          };
          polis = {
            type = "app";
            program = "${polis}/bin/polis";
          };
          polisctl = {
            type = "app";
            program = "${polisctl}/bin/polisctl";
          };
          controller = {
            type = "app";
            program = "${controller}/bin/polis-controller";
          };
          worker = {
            type = "app";
            program = "${worker}/bin/polis-worker";
          };
          demo-agent = {
            type = "app";
            program = "${demoAgent}/bin/polis-demo-agent";
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
