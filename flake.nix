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
          vendorHash = "sha256-rirtY4Xkg+z3Xu/5dI5IUlSQ4VocuMMSIU5VdKTrKRc=";
          subPackages = [
            "./cmd/polis"
            "./cmd/polisctl"
            "./cmd/polis-controller"
            "./cmd/polis-mailbox"
            "./cmd/polis-worker"
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
          unexecutableFd = pkgs.runCommand "fd-unexecutable" { } ''
            mkdir -p $out/bin
            printf '#!/bin/sh\nexit 0\n' > $out/bin/fd
            chmod 0444 $out/bin/fd
          '';
        in
        pkgs.buildNpmPackage {
          pname = "polis-pi-runtime";
          version = "0.1.0";
          src = self + /runtime/pi;
          nodejs = pkgs.nodejs_22;
          npmDepsFetcherVersion = 2;
          npmDepsHash = "sha256-t6EVHb9TX/vJfM3A6BSgKJP+8iBQHQBL+vGNK5JRawc=";

          nativeBuildInputs = [ pkgs.makeWrapper ];
          nativeCheckInputs = [
            pkgs.fd
            pkgs.ripgrep
          ];
          npmBuildScript = "build";
          doCheck = true;
          checkPhase = ''
            runHook preCheck
            export POLIS_PI_FD_PATH=${pkgs.fd}/bin/fd
            export POLIS_TEST_UNEXECUTABLE_FD=${unexecutableFd}/bin/fd
            node --test dist/test/*.test.js
            runHook postCheck
          '';

          installPhase = ''
            runHook preInstall
            mkdir -p $out/lib/polis-pi-runtime $out/bin
            cp -r dist node_modules package.json $out/lib/polis-pi-runtime/
            makeWrapper ${pkgs.nodejs_22}/bin/node $out/bin/polis-pi-agent \
              --add-flags "$out/lib/polis-pi-runtime/dist/src/main.js" \
              --set POLIS_PI_FD_PATH ${pkgs.fd}/bin/fd \
              --prefix PATH : ${pkgs.lib.makeBinPath [ pkgs.fd ]}
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
          mailbox = programFor system "polis-mailbox" "Polis mailbox service";
          worker = programFor system "polis-worker" "Polis worker";
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
                WorkingDir = "/workspace";
              };
            };
        in
        {
          default = programs;
          inherit
            polis
            polisctl
            controller
            mailbox
            worker
            ;
          pi-runtime = piRuntime;
          manifests = pkgs.runCommand "polis-kubernetes-manifests" { nativeBuildInputs = [ pkgs.kubectl ]; } ''
            kubectl kustomize ${self}/config/default > $out
          '';
          container = pkgs.dockerTools.buildLayeredImage {
            name = "polis";
            tag = "dev";
            contents = [
              controller
              mailbox
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
              ];
              Labels = {
                "org.opencontainers.image.description" = "Polis controller and mailbox services";
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
          manifests = pkgs.runCommand "polis-kubernetes-manifests-check" { nativeBuildInputs = [ pkgs.kubectl ]; } ''
            kubectl kustomize ${self}/config/default > $out
          '';
        }
      );

      apps = forAllSystems (
        system:
        let
          polis = programFor system "polis" "Agent capability CLI for Polis";
          polisctl = programFor system "polisctl" "Operator control CLI for Polis";
          controller = programFor system "polis-controller" "Polis controller";
          mailbox = programFor system "polis-mailbox" "Polis mailbox service";
          worker = programFor system "polis-worker" "Polis worker";
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
          mailbox = {
            type = "app";
            program = "${mailbox}/bin/polis-mailbox";
          };
          worker = {
            type = "app";
            program = "${worker}/bin/polis-worker";
          };
        }
      );

      devShells = forAllSystems (
        system:
        let
          pkgs = pkgsFor system;
          polisctl = programFor system "polisctl" "Operator control CLI for Polis";
        in
        {
          default = pkgs.mkShell {
            packages = [
              polisctl
              pkgs.curl
              pkgs.fd
              pkgs.go
              pkgs.gopls
              pkgs.gotools
              pkgs.jq
              pkgs.kubebuilder
              pkgs.kubectl
              pkgs.kustomize
              pkgs.nodejs_22
              pkgs.ripgrep
            ];
            shellHook = ''
              export POLIS_PI_FD_PATH=${pkgs.fd}/bin/fd
            '';
          };
        }
      );

      formatter = forAllSystems (system: (pkgsFor system).nixfmt);
    };
}
